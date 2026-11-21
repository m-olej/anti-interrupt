# Anti-Interrupt Conference Call System

**Core Restriction:** Strictly NO Lightweight Transactions (`LWT` / Paxos `IF NOT EXISTS`)

## Programming Language (Golang)

1. **Goroutines for High-Frequency Stress Testing:** System must simulate dozens or hundreds of concurrent conference participants emitting microphone metadata every 10–20 ms (50–100 Hz). In Java, traditional OS threads are heavy, whereas in Go you can spawn 5,000 independent participant goroutines with `time.Ticker` loops in a few megabytes of RAM with near-zero scheduling jitter.
2. **SDK Maturity (`gocql`):** The official Apache-donated Go driver (`gocql`) is battle-tested in production at massive scale. Crucially, it exposes **every single required low-level distributed primitive**:
   - Custom `RetryPolicy` interfaces.
   - Query-level `Idempotent(bool)` flags.
   - Token-aware host routing (`TokenAwareHostPolicy`) and per-query `Consistency` overrides (`ANY`, `ONE`, `QUORUM`, `ALL`).
   - Explicit client-side microsecond timestamp injection (`WithTimestamp(int64)`) to reproduce and defend against clock-skew anomalies.
3. **Single Static Binary Deployment:** During the lab defense, deploy and run the app across 3–4 separate lab machines. 

## Recommended Go Tech Stack & Libraries

| Component | Package / Library | Role in Project |
| --- | --- | --- |
| **Core Language** | **Go 1.23+** | Native `context`, `sync`, `atomic`, and `time.Ticker` primitives for lock-free local concurrency. |
| **Cassandra Driver** | `github.com/apache/cassandra-gocql-driver/v2` (or `github.com/gocql/gocql`) | Token-aware cluster connection, `Batch` execution, custom `RetryPolicy`, `Consistency` tuning, and `Idempotent` query marking. |
| **Query Builder (Optional)** | `github.com/scylladb/gocqlx/v2` | Wraps `gocql` to provide clean struct binding and named query parameters. |
| **CLI & Execution Modes** | `github.com/spf13/cobra` | Clean CLI subcommands (`app simulator`, `app arbiter`, `app auditor`, `app chaos-clock`) for the lab presentation. |
| **Terminal UI (Dashboard)** | `github.com/charmbracelet/bubbletea` + `lipgloss` | Renders a real-time split-screen console dashboard showing the Active Speaker, Waiting Queue, Noise Floor levels, and Cassandra Exception counters. |
| **Synthetic Audio Math** (optional) | `gonum.org/v1/gonum/stat/distuv` + `math/rand/v2` | Generates realistic Gaussian background noise, Ornstein-Uhlenbeck drift for baseline noise floors, and speech burst envelopes. |
| **Structured Logging** | `log/slog` (Standard Library) | High-speed non-blocking structured logging to record timestamp collisions, torn rows, and `WriteTimeoutException` retries during stress tests. |


## Project Scope: Anti-Interrupt Conference Call System

### System Overview

The **Anti-Interrupt Conference Call System** is a distributed acoustic telemetry and real-time floor-control arbitration engine backed by an Apache Cassandra cluster (deployed across 3–4 nodes with `replication_factor = 3`).

Rather than routing raw PCM audio bytes, distributed client nodes locally extract acoustic metadata frames at 50–100 Hz and stream them to Cassandra. Simultaneously, the nodes use Cassandra as a decentralized coordination backbone to:

1. Continuously calibrate each participant's dynamic background noise floor.
2. Arbitrate **who holds the speaking floor** based on speech onset order, priority tier, and historical speaking fairness—automatically muting/queuing interrupting participants **without using Lightweight Transactions (LWT)** or an external lock manager (like ZooKeeper/Redis).

## System Assumptions & How They Manifest Cassandra Shortcomings

Each assumption below is engineered to force a specific Cassandra architectural limitation to surface, paired with the design choice that resolves it.

### High-Frequency Acoustic Telemetry (100 Hz per Participant)

- **System Assumption:** Every active participant node emits a metadata frame every 10 ms (100 Hz) containing:
1. Categorized events: *A* (active), *S* (silence), *T* (background thud).
2. Optional overcomplicated 'realistic' metadata
   - `rms_energy` (dBFS amplitude),
   - `fundamental_freq` (F0​ in Hz to separate human voice 85–300 Hz from desk thuds),
   - `zcr` (Zero-Crossing Rate to separate vowel speech from high-frequency hiss),
   - `snr_margin` (dB above the user's current background noise floor),
   - `vad_confidence` (0.0–1.0 probability of active speech).


- **How It Affects Cassandra (The Shortcoming):**
   - **Wide Row Exhaustion & Hot Partitions:** Cassandra has a hard limit of 2×109 cells per partition and suffers severe compaction/heap degradation when partitions exceed 100 MB. A 50-person call at 100 Hz writing 5 columns generates 25,000 cells/second (90,000,000 cells/hour). Partitioning solely by `room_id` creates a massive single-node hotspot and eventually crashes the node.
   - **Tombstone Accumulation if Pruned Manually:** Deleting old telemetry frames via `DELETE` floods SSTables with tombstones, causing read timeouts during compaction.

- **Architectural Design Solution:**
   - **Composite Time-Bucketed Partition Key:** Partition telemetry by `PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)` where `minute_bucket` is formatted as `YYYY-MM-DDTHH:mm`. This bounds each partition to exactly 60 seconds of data and scatters historical buckets across the consistent hashing ring.
   - **Zero-Delete Ephemeral Retention (`USING TTL`):** All telemetry inserts include `USING TTL 600`, allowing Cassandra to expire old frames automatically during SSTable compaction without client-issued `DELETE` statements.

### Simultaneous Speech Onset & Lock-Free Floor Contention (No LWT)

- **System Assumption:** When the speaking floor is open, multiple participants may begin speaking within a 2–15 ms window of each other. Only one participant may be granted the `ACTIVE_SPEAKER` state; all others must detect they lost the race, mute their outbound audio stream, and enter an ordered `WAITING_QUEUE`. **Lightweight Transactions (`IF NOT EXISTS`) are strictly prohibited.**

- **How It Affects Cassandra (The Shortcoming):**
   - **Non-Atomic `SELECT`** → **`INSERT` & Lost Updates:** Under Last-Write-Wins (LWW), if User A and User B both execute `SELECT active_speaker FROM Rooms` (seeing `NULL`) and then execute `UPDATE Rooms SET active_speaker = ...`, the write with the higher timestamp silently overwrites the earlier speaker. Neither client realizes a collision occurred.

- **Architectural Design Solution:**
   - **Append-Only Clustering Queue + Write-Wait-Read Verification:**
      1. Instead of overwriting a single `active_speaker` cell, contenders insert a separate claim row into a `FloorClaims` table: `PRIMARY KEY ((room_id, epoch_bucket), priority_tier, claim_ts, user_id)`.
      2. Because `priority_tier`, `claim_ts`, and `user_id` are clustering columns, Cassandra sorts all concurrent claims deterministically on disk.
      3. **Optimistic Verification Protocol:** A client writes its claim at `ConsistencyLevel.QUORUM`, waits a short stabilization window (Δt=25 ms), and reads back the top rows of `FloorClaims` at `QUORUM`.
      4. If the client's `user_id` is at index `0` of the returned result set, it transitions to `SPEAKING`. If another user sits at index `0`, the client transitions to `MUTED_QUEUED` and respects the winner.

### Coupled Multi-Column State Updates & Timestamp Collisions

- **System Assumption:** Whenever a participant claims or updates the active floor state, the system must record coupled acoustic invariants: the `speaker_id`, their calibrated `noise_floor_db`, and their `active_gain_multiplier` (where `active_gain_multiplier` is inversely proportional to `noise_floor_db`, e.g., G=−1×noise_floor_db).

- **How It Affects Cassandra (The Shortcoming):**
   - **Cell-Level Lexicographical Tie-Breaking & Torn Rows:** If two threads/nodes update the same row key with the exact same microsecond timestamp, Cassandra resolves the conflict independently for *each column* using lexicographical byte order. Thread 1 might win `speaker_id` (e.g., `"Zofia" > "Adam"`), while Thread 2 wins `active_gain_multiplier`, leaving the row in a corrupted, physically impossible state (violating the invariant between speaker and gain).

- **Architectural Design Solution:**
   - **Atomic Cell Serialization (JSON / Frozen Payload):** In `RoomActiveState` table, serialized `speaker_state_json` into a single atomic  (or `blob`) column. Because LWW tie-breaking operates per cell, a timestamp collision is guaranteed to pick either 100% of Speaker A's state or 100% of Speaker B's state—never a torn mix.

### Dynamic Priority Scoring via Cumulative Speaking Time

- **System Assumption:** To prevent one person from monopolizing the call, the system tracks each participant's `total_speech_frames`, `interruption_attempts`, and `blocked_interruptions`. Participants who have spoken the least receive a higher `priority_tier` when contesting the floor.

- **How It Affects Cassandra (The Shortcoming):**
   - **Non-Idempotent Distributed Counters under Partition/Timeout:** Cassandra `counter` columns (`SET total_speech_frames = total_speech_frames + 1`) are **not idempotent**. If a network glitch or heavy load triggers a `WriteTimeoutException` or `OperationTimedOutException`, the client driver cannot know if the increment succeeded. Automatically retrying increments causes severe over-counting.

- **Architectural Design Solution:**
   - **Dual-Track Accounting (Counters vs. Idempotent Turn Logs):**
      1. We maintain a `RoomCallCounters` table using native Cassandra `counter` columns updated at `ConsistencyLevel.ONE` with `Idempotent(false)` in our custom `gocql.RetryPolicy` (preventing blind driver retries on timeout).
      2. For exact priority calculation that survives network partitions, each completed speech turn is written once to a `CompletedSpeechTurns` table keyed by `PRIMARY KEY ((room_id, user_id), turn_uuid)` with a static integer column `duration_ms`. Because writing a specific `turn_uuid` is **100% idempotent**, we mark this query `Idempotent(true)` and let the custom `RetryPolicy` aggressively retry across network partitions without ever double-counting.

### Multi-Dimensional Lookups & Post-Call Global Auditing

- **System Assumption:** Moderators need to query active rooms by participant (`"Which room is User X currently speaking in?"`), inspect a user's recent background noise calibration history, and generate a post-call **Global Audit Report** counting all processed frames across the entire cluster.

- **How It Affects Cassandra (The Shortcoming):**
   - **Query-Driven Schema Rigidity & `ALLOW FILTERING` Penalty:** Cassandra cannot filter or sort by non-key columns without `ALLOW FILTERING` (which scans the entire cluster).
   - **`COUNT(*)` 10,000-Row Ceiling & Token Pagination Duplicates:** Running `SELECT count(*)` fails or truncates on large telemetry tables, and paging across composite keys with `WHERE token(room_id, minute_bucket) >= ... LIMIT K` returns duplicate records at page boundaries when a partition contains multiple clustering rows.

- **Architectural Design Solution:**
   - **Denormalized Lookup Tables Synchronized via `Batch`:** Maintain a denormalized `ActiveRoomByUser` table updated alongside `FloorClaims` using a logged `gocql.Batch`. Store each user's last 10 noise floor calibrations in a Cassandra `list<double>` collection inside `SpeakerProfiles`.
   - **Token-Range Paginator with Client-Side Deduplication:** The `auditor` CLI mode implements a full-ring scanner using `token(room_id, minute_bucket)` pagination and maintains a sliding hash-set of seen `(room_id, minute_bucket, frame_ts, user_id)` keys in Go memory to strip boundary duplicates and compute exact cluster-wide totals.

## 5. Complete CQL Database Schema

```
CREATE KEYSPACE IF NOT EXISTS anti_interrupt
WITH REPLICATION = {
    'class' : 'SimpleStrategy',
    'replication_factor' : 3
};

USE anti_interrupt;

-- High-Frequency Acoustic Stream (Solves Wide-Row limit via minute_bucket + TTL)
CREATE TABLE audio_telemetry (
    room_id text,
    minute_bucket text,          -- Format: '2026-11-03T16:05'
    frame_ts timestamp,
    user_id text,
    -- 1. 
        event_category text,
    -- 2. 
        -- rms_energy double,
        -- fundamental_freq double,
        -- zcr double,
        -- snr_margin double,
        -- vad_confidence float,
    PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)
) WITH CLUSTERING ORDER BY (frame_ts DESC, user_id ASC);

-- Lock-Free Floor Control Queue (Solves Mutual Exclusion without LWT)
CREATE TABLE floor_claims (
    room_id text,
    turn_seq int,                -- Monotonic logical turn counter
        -- Optional
        -- priority_tier int,           -- Calculated by speaking history of a session
    claim_ts bigint,             -- Microsecond timestamp
    user_id text,
    claim_status text,           -- 'CLAIMING', 'SPEAKING', 'QUEUED', 'RELEASED'
        -- Optional:
        -- invariant_payload text,      -- Atomic JSON {"user":"...", "noise_db":-52.1, "gain":52.1} (Solves Zadanie 4)
    PRIMARY KEY ((room_id), turn_seq, priority_tier, claim_ts, user_id)
) WITH CLUSTERING ORDER BY (turn_seq DESC, priority_tier ASC, claim_ts ASC, user_id ASC);

-- Idempotent Speech Turn Ledger (Survives Network Partitions with Idempotent Retries)
CREATE TABLE completed_speech_turns (
    room_id text,
    user_id text,
    turn_id uuid,
    duration_ms int,
        -- Optional (when using realistic metadata)
        -- avg_rms_db double,
    PRIMARY KEY ((room_id, user_id), turn_id)
);

-- Distributed Live Call Counters
CREATE TABLE room_call_counters (
    room_id text,
    user_id text,
    speech_frames_count counter,
    interruption_attempts counter,
    muted_by_arbiter_count counter,
    PRIMARY KEY ((room_id), user_id)
);
```

## Program structure

### Call simulator
> Helper for testing without restarting (keeping benchmark data live in the server)

**Parameters**:
- *rooms*: number of conference rooms 
- *users-per-room*: how many goroutines will be spawned per room
- *freq*: frequency of events emitted by a goroutine
- *mode*: Test mode: 
    - *races*: shows speaker races (started at the same time), 
    - *fairness*: shows priority management (no domiminant speaker)
    - *random*: truly random 

### Auditor
> Core Backend API, receives all events from `Call simulator`

**Modules**:
1. Database interface
2. Simple HTTP server
3. Statistics (polling, deduplicating and summarizing cassandra data)
    - Active speaker
    - Room speaker turns with intervals (who: start stop)
    - room audio statistics (if using realistic metadata)
1. Spawner (goroutine lifetime, communication api)
    - orchestrates goroutines
5. Scheduler (modes implemented here)
    - dictates goroutine activity (start speaking, stop speaking)
6. Logger (comparing against database, debugging)

### TUI 
> Visualization frontend

**Modules**:
1. Fetch (pulls data from *Auditor*)
2. Display (visualizes data)

## Lab Defense & Stress-Test Scenarios 

**Tests**:
1. **Stress Stream**: A lot of users in few rooms emitting events at *100Hz* or more.
    - Show no loss of data (sent events == stored events)
    - Error handling for `WriteTimeoutException` and `Overloaded Exception`
2. **Race**: Multiple users in a room start speaking at the same time.
    - Show that *Last Write Wins* doesn't affect fairness
