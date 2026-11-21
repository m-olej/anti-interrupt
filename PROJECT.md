# Anti-Interrupt — Project Source of Truth

> **Authority.** This document is the canonical description of the project: its purpose, assumptions,
> architecture, data model, protocols, test modes, and measurement plan. 
> **How to update it.** Any change to a schema key, a protocol step, a run-lifecycle rule, or a
> lab-demonstration mechanism must be reflected here in the same change. Decisions are recorded in
> §13; unresolved items in §14.

---

## 0. Document map

| Document | Role |
|---|---|
| `PROJECT.md` | **Single source of truth**: scope, assumptions, architecture, schema, protocols, metrics |
| `IMPLEMENTATION_TASKS.md` | User-story backlog with context, implementation tips, testing, acceptance criteria |
| `PERF_THEORY.md` | General performance-engineering theory and tooling |
| `PERF_ANALYSIS.md` | Performance model and critique applied to this system |
| `README.md` | Operational quick start; must be kept consistent with this document |

---

## 1. Mission and success criteria

### 1.1 What the project is

A distributed **acoustic telemetry and floor-control system** backed by Apache Cassandra.
Participants emit small metadata frames at 50–100 Hz (the payload is *not* audio; it is a compact
categorical/derived metadata frame). Participants also coordinate who holds the speaking floor
through Cassandra itself — **without Lightweight Transactions and without an external lock service**
(no Redis, ZooKeeper, etc.).

The project is a **study of Cassandra's architectural limits** and of concrete, correct ways to work
around them. The important output is not a production conferencing product; it is:

1. a working client/runtime that exercises Cassandra under realistic stress,
2. reproducible benchmark data, and
3. an analysis of whether each proposed workaround is correct under the stated adversarial
   conditions.

### 1.2 Success criteria

| # | Criterion |
|---|---|
| S1 | All six assumptions in §4 are implemented and demonstrable in a repeatable lab scenario. |
| S2 | No LWT (`IF NOT EXISTS`, `SERIAL`, Paxos) and no external lock manager appear anywhere in the domain path. |
| S3 | Every benchmark run produces structured, comparable data (per-client, per-stage, per-assumption evidence). |
| S4 | The system runs as a **single binary** with subcommands, deployable on 3–4 lab machines. |
| S5 | The TUI shows near-real-time run state by reading Cassandra; it does not depend on a live API process. |
| S6 | Parallel stress tests can be started on multiple machines against one cluster, with a shared run id. |
| S7 | The analysis explicitly states correctness limits, threats to validity, and the failure envelope of each workaround. |

### 1.3 Non-goals

- No raw PCM/audio streaming.
- No LWT, no Paxos, no external coordination service.
- No `DELETE` on floor/claim state as part of normal operation.
- No `ALLOW FILTERING` in any production/domain query path.
- No data-plane proxy or gateway between clients and Cassandra. (See D1/D2 in §13.)
- No multi-datacenter deployment; the lab is one DC, 3 nodes, RF=3.
- No attempt to make Cassandra behave like a relational/OLTP database.

---

## 2. System in one page

### 2.1 Roles

| Role | Process | Responsibility |
|---|---|---|
| **Participant runtime** | `run` (one-shot) or inside `agent` | Emits telemetry, runs claim protocol, maintains heartbeats, records local metrics |
| **Agent** | `agent` | Control plane: accepts run requests over HTTP, starts/stops participant runtimes, enforces resource limits, pushes snapshots |
| **Observer** | inside `agent` (toggleable) | Pulls domain state and cluster diagnostics on a low-frequency cycle, writes snapshots |
| **TUI** | `tui` | Reads `bench_latest`/`bench_snapshots` and renders; no API dependency |
| **Auditor** | `audit` | Full-ring token scan, deduplication, exactness report |
| **Reporter** | `report` | Aggregates run results into comparable tables/plots; offline |
| **Orchestrator** | `orchestrator` (optional) | Fans out run requests to many agents with one run id and a common start time |

### 2.2 Control plane vs data plane

```
                    control plane (HTTP, small, async)
  operator / orchestrator ──────────────► agent (one per lab machine)
                                              │
                                              │ starts participant runtimes (goroutines)
                                              ▼
                                       participant runtime ────────► Cassandra cluster
                                              │  telemetry writes          (3 nodes, RF=3)
                                              │  claim write-wait-read
                                              │  heartbeats / turn ledger / counters
                                              │
                                              │ push snapshots (1–5 Hz)
                                              ▼
                                         bench_latest / bench_snapshots
                                              ▲
                                              │ read only
                                            tui

  observer loop (inside agent) ─── pulls domain state + cluster diagnostics (1–2 Hz)
```

**Rule:** data never flows through the agent. The agent starts work and reports; it does not proxy
telemetry. This preserves benchmark fidelity and removes a central bottleneck.

### 2.3 Deployment topology

- Cassandra 4.1, 3 nodes, `SimpleStrategy`, `replication_factor=3`, one datacenter.
- 3–4 lab machines: each runs one `agent`; one may also run the TUI/auditor/reporter.
- NTP/chrony enabled by default. Clock-skew scenarios are simulated **inside the client**
  (`--clock-offset`), not by changing the OS clock, so they are reproducible and safe.
- Development: `docker compose up -d --wait`; `make build`; one binary.

---

## 3. Domain model

### 3.1 Entities and invariants

| Entity | Identity | Key invariant |
|---|---|---|
| Room | `room_id` | One active floor at a time |
| Participant | `(room_id, user_id)` | A participant is in at most one room at a time |
| Telemetry frame | `(room_id, minute_bucket, frame_ts, user_id)` | One row per emitted frame; immutable |
| Claim | `(room_id, turn_seq, priority_tier, claim_ts, user_id)` | A claim for a given turn is unique by this key |
| Turn | `(room_id, turn_seq)` | Exactly one winner (index 0) per contested turn |
| Heartbeat | `(room_id, turn_seq, user_id)` | Present while the speaker is alive; TTL-bound |
| Completed turn | `(room_id, user_id, turn_id)` | Written once; idempotent by `turn_id` |
| Counter row | `(room_id, user_id)` | Best-effort aggregate; may drift under timeout + retry |
| Noise profile | `(room_id, user_id)` | Last N calibration samples; bounded list |
| Run | `run_id` | One logical benchmark across one or more agents |
| Client run | `(run_id, client_id)` | One agent's participation in a run |

### 3.2 Telemetry event categories

The canonical telemetry payload is categorical, not acoustic:

| Category | Meaning |
|---|---|
| `A` | Active speech |
| `S` | Silence |
| `T` | Background thud |

The optional "realistic" profile adds `rms_energy`, `fundamental_freq`, `zcr`, `snr_margin`,
`vad_confidence`. It is a **schema extension**, not the baseline; the benchmark's write-rate and
partitioning behavior is identical. The lab demonstration cares about rate, partition size, and
TTL behavior — not acoustic realism.

### 3.3 Floor semantics

- A **turn** is identified by a monotonically increasing `turn_seq` per room.
- A turn is **open** when no live heartbeat exists for the current turn and the turn row is
  `RELEASED` (or no row exists).
- Contenders race to claim `turn_seq = current + 1`.
- **Winner** = index 0 of the ordered read (priority tier, then claim timestamp, then user id).
- Losers queue; they do not overwrite anything and do not retry the same turn blindly.
- **Release** writes a `RELEASED` row for the turn; it never deletes.
- **Liveness** is a heartbeat row with TTL 2 s, refreshed every 500 ms.
- A stale speaker (heartbeat expired) loses the floor; the next turn can proceed.

---

## 4. The six canonical assumptions

Each assumption is a deliberate adversarial condition. The implementation must **exercise** the
shortcoming and then **handle** it correctly — not design it away.

### A1 — High-frequency telemetry (100 Hz per participant)

**Assumption.** Each participant emits a frame every 10 ms (50–100 Hz). A 50-person room at 100 Hz
writes 5,000 frames/s.

**Shortcoming.** A partition keyed only by `room_id` creates an unbounded, hot partition; Cassandra
degrades as partitions exceed roughly 100 MB (practical guidance), independently of the hard
2×10⁹-cell limit. Manual deletes for retention flood SSTables with tombstones.

**Solution.** Composite key `((room_id, minute_bucket), frame_ts, user_id)` bounds the partition in
*time*; `USING TTL` bounds retention without client deletes.

**What must be demonstrated.** Sustained write rate, partition sizes across buckets, latency and
error handling under load, TTL expiry without tombstones, and the point at which partition size
(not cell count) becomes the problem.

**Known limitation.** Time bucketing does not bound *size*: size = users × frequency × 60 s ×
bytes/row. At high users/room, a bucket can exceed the guidance; see D4 and the scale scenario.

### A2 — Simultaneous onset and lock-free floor contention

**Assumption.** Multiple participants may start speaking within 2–15 ms. Exactly one must hold the
floor; the rest queue. LWT is forbidden.

**Shortcoming.** `SELECT`-then-`UPDATE` is not atomic; last-write-wins silently loses one claimant
and no client detects the collision.

**Solution.** Append-only claims at a given `turn_seq`, ordered deterministically by
`(priority_tier ASC, claim_ts ASC, user_id ASC)`; write at `QUORUM`, wait a stabilization window,
read the top rows at `QUORUM`, and interpret index 0 as the winner.

**What must be demonstrated.** Under synchronized onsets, exactly one winner per contested turn;
losers transition to queued; no LWT in the query log; measured acquisition latency; no overwrite
of an existing turn.

**Known limitation.** The stabilization window (25 ms) is a latency floor and a per-room throughput
cap (~40 claims/s); intra-turn clock skew can still misorder claims (see A3).

### A3 — Desynchronized node clocks / no DELETE on floor state

**Assumption.** Clocks can drift; a chaos mode shifts a client's timestamps by ±60 s.

**Shortcoming.** A `DELETE` with a future timestamp writes a tombstone that shadows later writes
until the future time passes — a "delete from the future" deadlock. Overwriting a single cell with
a future timestamp has the same shadowing risk.

**Solution.** **No DELETE for floor state.** Every turn is a new clustering coordinate
`turn_seq`; a future timestamp on turn N cannot shadow writes to turn N+1. Liveness uses
heartbeats with TTL 2 s. Release writes `RELEASED`, never deletes.

**What must be demonstrated.** With ±60 s offsets, a release followed by a new claim completes
without a deadlock; `turn_seq` advances; stale heartbeats expire; no DELETE statement exists in the
domain code path.

**Known limitation.** Within one turn, a future `claim_ts` can still win the race unfairly. That is
a deliberate, documented consequence of client-side timestamps and is part of the analysis, not a
bug to hide.

### A4 — Coupled multi-column updates and torn rows

**Assumption.** The active-speaker state must keep `speaker_id`, `noise_threshold_db`, and
`inverse_gain_db` consistent (`noise + inverse = 0`).

**Shortcoming.** With identical microsecond timestamps, LWW tie-breaking is per cell and
lexicographic; different columns can come from different writers, producing an impossible state.

**Solution.** Two tables:
- `room_active_state_naive` — multi-column, used to **demonstrate** torn rows under forced timestamp
  collisions;
- `room_active_state` — the coupled fields serialized into one atomic `speaker_state_json` cell, so a
  collision picks one entire state, never a mix.

**What must be demonstrated.** A forced-collision scenario produces invariant violations in the
naive table and zero in the corrected table, with the same workload and timestamps.

### A5 — Dynamic priority and non-idempotent counters

**Assumption.** Priority depends on cumulative speaking time, interruption attempts, and blocked
interruptions. Counters are updated frequently.

**Shortcoming.** Cassandra counters are read-modify-write per replica and non-idempotent. A timeout
followed by a blind retry can double-count.

**Solution.** Dual-track:
- `room_call_counters` for live, best-effort aggregates at `CL=ONE`, marked non-idempotent, with
  **no blind driver retry**; updates are aggregated (per turn or per second), never per frame.
- `completed_speech_turns` keyed by `turn_id` UUID for exact, retry-safe accounting; marked
  idempotent and retried aggressively.

**What must be demonstrated.** Under injected timeouts, counter totals drift above the exact turn
ledger when retries occur; the ledger remains exact. The analysis quantifies the drift and explains
the retry policy.

### A6 — Multi-dimensional lookups and global auditing

**Assumption.** Moderators need "which room is user X in?", a user's recent noise calibration
history, and an exact cluster-wide count of processed frames.

**Shortcoming.** Cassandra cannot filter/sort by non-key columns without `ALLOW FILTERING` (a
cluster scan). `COUNT(*)` is unreliable at scale, and token-range pagination can return duplicate
rows at page boundaries.

**Solution.**
- `active_room_by_user` — denormalized lookup updated alongside claims via a logged batch.
- `speaker_profiles` — bounded `list<double>` of recent noise samples and a `map<text,text>` of
  device metadata (demonstrates collections).
- Token-range paginator in `audit` with a client-side seen-set to strip boundary duplicates and
  produce exact counts within a bounded window.

**What must be demonstrated.** The lookup answers without `ALLOW FILTERING`; a deliberately
filtering query is shown to be rejected/slow as a negative demo; the auditor's deduplicated count
matches the ingestion ledger for a bounded run, and the duplication rate without dedupe is
measured.

---

## 5. Data model

### 5.1 Conventions

- Keyspace: `anti_interrupt` (single DC). Benchmark metadata lives in the same keyspace with a
  `bench_` prefix (see D8).
- Time: UTC. `minute_bucket` is text `YYYY-MM-DDTHH:mm` (readable in `cqlsh`; see D4 for the
  storage trade-off).
- `frame_ts`: `timestamp` (microsecond precision from the client).
- `claim_ts`: `bigint` microseconds since epoch (explicit client timestamp; subject to skew by
  design).
- IDs: `room_id`/`user_id`/`client_id` are text for readability; `turn_id`/`run_id` are UUIDs.
- TTLs: telemetry 600 s; heartbeats 2 s; benchmark snapshots 24 h; benchmark latest 7 d; run
  metadata/results retained until manually cleaned (or 30 d in a production profile).
- **No LWT, no `DELETE` on floor state, no `ALLOW FILTERING` in domain paths.**
- All writes are upserts keyed by natural keys; telemetry and turn rows are idempotent.

### 5.2 Lab schema (canonical DDL)

```cql
CREATE KEYSPACE IF NOT EXISTS anti_interrupt
WITH REPLICATION = {
  'class': 'SimpleStrategy',
  'replication_factor': 3
};

USE anti_interrupt;

-- A1: high-frequency telemetry. Canonical minimal payload.
-- Partition size is bounded in TIME, not in SIZE; see D4.
CREATE TABLE audio_telemetry (
  room_id        text,
  minute_bucket  text,          -- 'YYYY-MM-DDTHH:mm' UTC
  frame_ts       timestamp,
  user_id        text,
  event_category text,          -- 'A' | 'S' | 'T'
  PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)
) WITH CLUSTERING ORDER BY (frame_ts DESC, user_id ASC);

-- Optional "realistic" profile (separate migration; not baseline):
--   rms_energy double, fundamental_freq double, zcr double,
--   snr_margin double, vad_confidence float

-- A2/A3: append-only floor claims. New turn_seq per turn; no DELETE.
CREATE TABLE floor_claims (
  room_id           text,
  turn_seq          int,
  priority_tier     int,        -- 1 moderator, 2 high fairness, 3 standard
  claim_ts          bigint,     -- client microseconds; may be skewed
  user_id           text,
  claim_status      text,       -- CLAIMING | SPEAKING | QUEUED | RELEASED
  invariant_payload text,       -- atomic JSON {user, noise_db, gain}
  PRIMARY KEY ((room_id), turn_seq, priority_tier, claim_ts, user_id)
) WITH CLUSTERING ORDER BY (turn_seq DESC, priority_tier ASC, claim_ts ASC, user_id ASC);

-- A3: liveness without DELETE. One row per active turn; TTL 2 s, refreshed every 500 ms.
CREATE TABLE floor_heartbeats (
  room_id   text,
  turn_seq  int,
  user_id   text,
  seen_at   timestamp,
  PRIMARY KEY ((room_id), turn_seq, user_id)
) WITH default_time_to_live = 2;

-- A4: deliberately vulnerable table for the torn-row demonstration.
CREATE TABLE room_active_state_naive (
  room_id            text PRIMARY KEY,
  active_user_id     text,
  noise_threshold_db double,
  inverse_gain_db    double      -- invariant: noise_threshold_db + inverse_gain_db == 0
);

-- A4: corrected table. Coupled invariant lives in one atomic cell.
CREATE TABLE room_active_state (
  room_id            text PRIMARY KEY,
  turn_seq           int,
  speaker_state_json text,       -- {"user":..., "noise_db":..., "gain":...}
  updated_at         timestamp
);

-- A6: bounded collection history (list + map demonstration).
CREATE TABLE speaker_profiles (
  room_id              text,
  user_id              text,
  base_noise_floor_db  double,
  recent_noise_samples list<double>,   -- keep the last 10
  device_metadata      map<text, text>,
  PRIMARY KEY ((room_id), user_id)
);

-- A5: exact, idempotent turn ledger.
CREATE TABLE completed_speech_turns (
  room_id    text,
  user_id    text,
  turn_id    uuid,
  duration_ms int,
  avg_rms_db double,            -- optional/realistic profile
  PRIMARY KEY ((room_id, user_id), turn_id)
);

-- A5: best-effort counters. Aggregate before writing; never per frame.
CREATE TABLE room_call_counters (
  room_id               text,
  user_id               text,
  speech_frames_count   counter,
  interruption_attempts counter,
  muted_by_arbiter_count counter,
  PRIMARY KEY ((room_id), user_id)
);

-- A6: denormalized "which room is this user in?" lookup.
CREATE TABLE active_room_by_user (
  user_id  text PRIMARY KEY,
  room_id  text,
  turn_seq int,
  state    text,                -- SPEAKING | QUEUED | IDLE
  since    timestamp
);
```

### 5.3 Benchmark/observability schema

These tables are written by the agent/observer, not by the participant data path. They are the
basis for the TUI and the analysis.

```cql
CREATE TABLE bench_runs (
  run_id         uuid PRIMARY KEY,
  created_at     timestamp,
  scenario       text,          -- JSON: rooms, users_per_room, freq, mode, seed, duration
  git_commit     text,
  schema_version int,
  status         text,          -- pending | running | done | failed | cancelled
  started_at     timestamp,
  ended_at       timestamp,
  notes          text
);

CREATE TABLE bench_clients (
  run_id           uuid,
  client_id        text,
  host             text,
  status           text,
  started_at       timestamp,
  ended_at         timestamp,
  last_seen        timestamp,
  events_generated bigint,
  events_acked     bigint,
  errors           bigint,
  retries          bigint,
  dropped          bigint,
  summary          text,        -- JSON: p50/p95/p99, per-stage, error classes
  PRIMARY KEY (run_id, client_id)
);

-- 1–5 Hz live snapshots for the TUI. TTL keeps the table bounded.
CREATE TABLE bench_snapshots (
  run_id         uuid,
  client_id      text,
  ts             timestamp,
  schema_version int,
  payload        text,          -- JSON
  PRIMARY KEY ((run_id, client_id), ts)
) WITH CLUSTERING ORDER BY (ts DESC)
  AND default_time_to_live = 86400;

-- Latest snapshot per client; one query returns the whole run.
CREATE TABLE bench_latest (
  run_id   uuid,
  client_id text,
  ts       timestamp,
  payload  text,                -- JSON
  PRIMARY KEY (run_id, client_id)
) WITH default_time_to_live = 604800;   -- 7 days since last update
```

`payload` is JSON with a `schema_version` field. JSON is intentional: a few KB at 1–5 Hz is cheap,
and debuggability matters more than compactness here. If size ever becomes a problem, the column can
become a blob without changing the access pattern.

### 5.4 Access patterns

| Query | Table | Notes |
|---|---|---|
| Frames for a room in a minute | `audio_telemetry` | Single-partition slice, `frame_ts DESC` |
| Latest turn / top claims | `floor_claims` | `WHERE room_id=? LIMIT K`, clustering order gives the answer |
| Is the speaker alive? | `floor_heartbeats` | Row exists → alive; TTL handles expiry |
| Current floor state | `room_active_state` | One row per room |
| Which room is user X in? | `active_room_by_user` | One row per user |
| Recent noise history | `speaker_profiles` | Bounded list |
| Exact turn history | `completed_speech_turns` | Per user partition |
| Counter totals | `room_call_counters` | Best-effort, may drift |
| TUI live state | `bench_latest` | One query per run, keyed by `run_id` |
| TUI history | `bench_snapshots` | Bounded slice per client |
| Run comparison | `bench_runs` + `bench_clients` | Offline analysis |

### 5.5 Partition sizing rules

- `audio_telemetry`: rows per bucket = `users_per_room × frequency × 60`. Target **≤ 30–50 MB per
  bucket** in the lab. If a scenario exceeds it, reduce the bucket or add a hash shard
  (`shard = hash(user_id) % K`) — at the cost of fan-out on reads.
- `floor_claims`: one partition per room for the duration of the run. Claims are capped by the
  stabilization window (≤ ~40/s/room), so a 1 h run is ~144k rows. For long-lived rooms, add a
  `session_bucket` to the partition key (see §14).
- `floor_heartbeats`: one row per turn/user with a 2 s TTL; bounded by active speakers.
- `bench_snapshots`: one partition per `(run_id, client_id)`; bounded by TTL and snapshot rate.
- Everything else is small per-key state.

### 5.6 Migrations and versioning

- Migrations are embedded in `cql/` and applied by one migrator process, never concurrently by
  every agent.
- Agents verify the applied schema version with the benchmark tables and refuse to start on
  mismatch.
- The "realistic" telemetry profile, hash shards, or new benchmarks tables are added as new
  migrations; never edit an applied migration.
- Migration errors must be fatal to the migrator (the current code logs and continues; that is a
  bug).

---

## 6. Protocols

### 6.1 Telemetry ingestion

1. The participant generator produces frames at the configured frequency.
2. Frames are batched (size-or-time: e.g., 10–50 frames or 10–50 ms) to amortize HTTP/driver
   overhead. (Telemetry has no user-facing latency SLO; batching is free.)
3. Each frame is written with `USING TTL 600` at `CL=ONE` or `QUORUM` depending on the scenario;
   the choice must be recorded in the run metadata.
4. Writes are idempotent by `(room_id, minute_bucket, frame_ts, user_id)`; retries are safe.
5. Dropped ticks and rejected batches are counted, never silently discarded.
6. Partition key uses the **UTC minute of `frame_ts`**, not the local wall clock.

### 6.2 Claim acquisition

1. Read the latest turn for the room (`turn_seq DESC LIMIT 1`) to determine the next open turn.
2. Insert a claim row at `(room_id, next_turn, priority_tier, claim_ts, user_id)` with
   `claim_status=CLAIMING`, `CL=QUORUM`, idempotent.
3. Wait the stabilization window Δt (default 25 ms; configurable and recorded).
4. Read the top `K` rows for that turn at `CL=QUORUM`.
5. If own `user_id` is index 0 → transition to `SPEAKING`; otherwise `QUEUED` (record position).
6. The winner writes `room_active_state` (and, only in the A4 demo, the naive table) and begins
   heartbeats.
7. If the winner does not start heartbeats within 2 s, the turn is considered stale and a later
   contender may proceed.

### 6.3 Heartbeats, release, and staleness

- Winner writes `floor_heartbeats (room_id, turn_seq, user_id, seen_at)` every 500 ms with TTL 2 s.
- Release writes `claim_status=RELEASED` on the turn row and stops heartbeats. **No DELETE.**
- Readers treat a turn as open when the turn is `RELEASED`/absent and no live heartbeat exists.
- The observer records stale-speaker events as a correctness metric.

### 6.4 Priority and fairness

- `priority_tier` is computed from cumulative speaking time: `1` moderator, `2` participants below
  the fairness target, `3` otherwise. The exact rule must be deterministic and documented in the
  fairness scenario.
- Ordering within a turn: tier ascending, then `claim_ts` ascending, then `user_id` ascending.
- Fairness is evaluated over completed turns, not frames.
- The analysis reports starvation (max wait), tier distribution, and speaking-time variance.

### 6.5 Counters and the idempotent ledger

- Counters (`speech_frames_count`, `interruption_attempts`, `muted_by_arbiter_count`) are updated
  **once per turn or per second**, never per frame, at `CL=ONE`, marked non-idempotent.
- No blind retry on timeout for counters; the error is recorded and surfaced.
- Every completed turn writes one `completed_speech_turns` row keyed by a UUID; this is idempotent
  and aggressively retryable.
- Priority is computed from the exact ledger where correctness matters; counters are monitoring.

### 6.6 Denormalized lookup and batch

- On claim/release, update `active_room_by_user` alongside the claim using a **logged batch**.
- A logged batch is atomic in the sense that all mutations are eventually applied, but it is not
  isolated and cross-partition batches are expensive; the analysis must state this.
- Readers reconcile a missing/stale lookup row by reading `floor_claims` for the room.

### 6.7 Audit

- Iterate the token ring over `audio_telemetry` using token-range queries with a stable page size.
- Maintain a seen-set of `(room_id, minute_bucket, frame_ts, user_id)` to strip boundary
  duplicates.
- Compare the deduplicated count to the run's generated/acked ledger.
- Exactness is scoped: define a max window/row budget; beyond it, use pre-aggregated counters and
  document the approximation. (See §10.4.)

### 6.8 Run lifecycle (control plane)

```
scheduled ──► running ──► done
    │            │
    │            ├──► failed
    │            └──► cancelled
    └──► stale (no heartbeat/lease)
```

- `POST /v1/runs` validates, registers, returns `202` with `{run_id, client_id, status}`; never
  blocks for the run duration.
- `run_id` is accepted from the caller (orchestrator) or generated; `client_id` defaults to hostname.
- `start_at` (optional, RFC3339) + NTP gives synchronized multi-agent starts.
- `duration` or `max_events` is mandatory; a run that never ends is a bug.
- `DELETE /v1/runs/{id}` cancels via the run's context.
- The agent writes a lease/heartbeat (`bench_clients.last_seen`); a stale run is detectable.
- Graceful shutdown cancels all runs, flushes final summaries, and closes the session.

### 6.9 Snapshot push and diagnostics pull

- Each participant runtime pushes a JSON snapshot to `bench_latest` and `bench_snapshots` at 1–5 Hz
  from its **in-memory** counters (no extra reads of the hot tables).
- The observer pulls domain state (top claims, active speaker, queue) at 1–2 Hz, bounded per room.
- The observer pulls cluster diagnostics at ≤1 Hz, best-effort, with a short timeout and a separate
  low-consistency session; skipped ticks are recorded. Cassandra 4.1 `system_views` tables are
  node-local, so per-node visibility needs per-node sessions or JMX/Prometheus.
- The observer's query rate and cost are recorded as run metadata; it is load on the system under
  test.

---

## 7. Component architecture

### 7.1 Repository layout (target)

```
cmd/anti-interrupt/main.go        # cobra root, one context, ExecuteContext
internal/config/                  # flag/env/scenario loading, validation
internal/cassandra/               # session factory, retry policy, idempotency, token awareness
internal/schema/                  # embedded migrations, version check
internal/domain/                  # Room, Participant, Claim, Turn, Frame, Run types
internal/telemetry/               # frame generation, batching, TTL writes
internal/arbiter/                 # claim state machine, heartbeats, fairness
internal/accounting/              # counters + completed-turn ledger
internal/auditor/                 # token-range paginator, dedupe, reports
internal/agent/                   # HTTP control plane, run registry, lifecycle
internal/runtime/                 # participant runtime orchestration (shared by run/agent)
internal/observer/                # domain + cluster diagnostic samplers
internal/stats/                   # bench_* writers, payload schema/versioning
internal/metrics/                 # histograms, counters, local NDJSON, summary
internal/chaos/                   # clock offset, timeout/latency injection, collision mode
internal/tui/                     # read bench_latest/snapshots, render
internal/report/                  # offline aggregation/comparison
cql/                              # embedded migrations
```

Dependencies point inward: `domain` depends on nothing; `telemetry`/`arbiter`/`accounting` depend on
`cassandra` + `domain`; `runtime` composes them; `agent` composes `runtime`; `tui`/`report` are
read-only consumers. No package imports `agent` except `cmd`.

### 7.2 Commands

| Command | Purpose |
|---|---|
| `anti-interrupt migrate` | Apply migrations once; verify version |
| `anti-interrupt run` | One-shot participant runtime (the "simple client") |
| `anti-interrupt agent` | HTTP control plane + runtime + optional observer |
| `anti-interrupt tui` | Read benchmark tables and render |
| `anti-interrupt audit` | Token-range exact/bounded audit for a run |
| `anti-interrupt report` | Offline aggregation/comparison |
| `anti-interrupt orchestrator` | Optional: fan out one run to N agents |
| `anti-interrupt backend` | Deprecated alias for `agent` (kept for compatibility) |

All commands share one root context from `signal.NotifyContext` and honor `SIGINT`/`SIGTERM`.

### 7.3 Agent HTTP contract

| Method | Path | Behavior |
|---|---|---|
| `POST` | `/v1/runs` | Validate, register, `202`; idempotent by `(run_id, client_id)`; `409` if already running |
| `GET` | `/v1/runs` | List local runs and statuses |
| `GET` | `/v1/runs/{id}` | Status, counters, last error |
| `DELETE` | `/v1/runs/{id}` | Cancel |
| `GET` | `/healthz` | Liveness/readiness (Cassandra reachable) |
| `GET` | `/metrics` | Prometheus/expvar |
| `GET` | `/debug/pprof/*` | Development only; disabled by default in lab mode |

Request body: `run_id?`, `client_id?`, `start_at?`, `duration`, `scenario{rooms, users_per_room,
freq, mode, seed, clock_offset?}`.

### 7.4 Concurrency model

- One gocql session per process, created at startup, health-checked, shared by all runs.
- Per-run `context.Context`; cancellation propagates to generators, claim loops, and writers.
- Per-run worker pools and bounded queues; no goroutine per frame.
- A run registry guarded by a mutex/atomic; no duplicate `(run_id, client_id)`.
- The observer is a separate goroutine with its own cadence and session settings.
- Graceful shutdown: stop accepting, cancel runs, flush summaries, close session.

### 7.5 Error, retry, and idempotency policy

| Operation | Idempotent | Retry |
|---|---|---|
| Telemetry upsert | yes | capped, jittered |
| Claim insert | yes (unique key) | capped; re-read before re-insert |
| Heartbeat upsert | yes | best-effort; losing a beat is tolerable |
| Release row | yes | capped |
| Completed turn | yes (UUID) | aggressive |
| Counter increment | **no** | **no blind retry**; record and surface |
| Lookup batch | effectively yes (same values) | capped; reconcile on read |

Timeouts: per-request deadlines (sub-second), never the driver default 11 s, except in a
deliberately recorded slow scenario.

### 7.6 Security (lab-appropriate)

- Bind the agent to localhost by default; explicit `--listen` to expose.
- Optional bearer token for `/v1/*`; `/healthz` may remain open.
- Validate every parameter (positive integers, bounded frequency, bounded users, known mode).
- Never interpolate user input into CQL identifiers; the keyspace/RF come from validated config.

---

## 8. Configuration

### 8.1 Global

| Setting | Default | Notes |
|---|---|---|
| `--nodes` | `127.0.0.1:9042` | Comma-separated |
| `--keyspace` | `anti_interrupt` | Validated identifier |
| `--rf` | `3` | Used only at bootstrap; changing it later requires `ALTER KEYSPACE` |
| `--log-level` | `info` | JSON `slog` |
| `--data-dir` | `./runs` | Local NDJSON/profile artifacts |

### 8.2 Scenario (run request / `run` flags)

| Setting | Default | Notes |
|---|---|---|
| `rooms` | 4 | Positive |
| `users_per_room` | 10 | Positive; capped per agent |
| `freq` | 100 | Hz per user; validated |
| `mode` | `random` | `random`, `races`, `fairness`, `skew`, `collision`, `timeout`, `audit`, `failure`, `scale` |
| `seed` | random | Recorded; makes runs comparable |
| `duration` | required for agent | Go duration; no infinite runs |
| `start_at` | now | RFC3339; used for synchronized starts |
| `clock_offset` | 0 | Per-client simulated skew (not OS clock) |
| `delay_ms` | 25 | Stabilization window; recorded |
| `telemetry_cl` | `ONE` | `ONE` or `QUORUM`; recorded |
| `batch_frames` / `batch_ms` | 10 / 25 | Telemetry batching |
| `observe` | true | Observer loop on/off |

### 8.3 Resource caps

- Max concurrent runs per agent (default 1; configurable for stress).
- Max goroutines/participants per agent; reject with `422` rather than OOM.
- Max batch bytes; max queue depth; 503/429 when saturated.
- Caps are recorded in the run metadata; "no artificial scenario limits" does not mean unbounded
  resource use.

---

## 9. Test modes and lab matrix

| Mode | What it exercises | Primary evidence |
|---|---|---|
| `random` | Baseline mixed traffic | Throughput, latency, no loss |
| `races` | Synchronized onsets within 2–15 ms | Exactly one winner per turn; queue correctness |
| `fairness` | Skewed speaking history/tiers | Priority ordering, starvation, variance |
| `skew` | ±60 s client clock offset | No deadlock, turn_seq advances, heartbeats expire |
| `collision` | Forced identical `claim_ts` | Naive table torn-row rate > 0; corrected = 0 |
| `timeout` | Injected write timeouts | Counter drift > 0; ledger exact |
| `audit` | Page-boundary duplication | Deduped count exact; duplicate rate measured |
| `failure` | Node down / latency / loss | QUORUM behavior, retries, recovery |
| `scale` | Users/room sweep | Partition size, hot partition, compaction |

Each mode is deterministic with a seed and records its parameters in `bench_runs.scenario`.

**Chaos injection is application-level** (client-side clock offset, forced timestamps, injected
timeouts, delayed/error responses) so runs are reproducible. Infrastructure chaos (`docker stop`,
`tc netem`) is allowed but must be recorded separately.

---

## 10. Measurement and analysis

### 10.1 Metric taxonomy

| Group | Metrics |
|---|---|
| Ingest | generated, attempted, acked, failed, retried, dropped, batch size, p50/p95/p99 |
| Claims | attempts, wins, losses, queue position, time-to-acquire, stale speakers, priority distribution |
| Correctness | multi-winner events, torn-row violations, counter drift, audit exactness, duplicate rows, missing frames |
| Cassandra | write/read p50/p95/p99, timeouts, unavailable, thread-pool drops, pending compactions, partition sizes, GC pauses |
| System | CPU, RSS, goroutines, GC cycles, network bytes |
| Run | duration, concurrency, seed, start offset, clock offsets, git commit, schema version |

### 10.2 What makes a run analyzable

- Every run has `run_id`, `client_id`, seed, scenario, git commit, and environment capture.
- Each client writes local NDJSON samples and a final summary; `bench_clients.summary` mirrors the
  summary for cross-machine querying.
- Percentiles are computed from raw histograms, not from 1 Hz snapshots.
- Open-loop generation is available for tail-latency measurement; closed-loop is recorded as such.
- The observer's own load is recorded and excluded from (or explicitly included in) the analysis.

### 10.3 Correctness oracles

- **A2:** `multiple_winners_per_turn == 0`; loser count = contender count − 1.
- **A3:** release→next-claim completes under ±60 s offsets; no DELETE statements; stale heartbeat
  detected.
- **A4:** naive invariant violations > 0 under collision; corrected == 0; the same input produces
  both.
- **A5:** exact ledger count == completed turns; counter total ≥ ledger under retries, with drift
  quantified.
- **A6:** deduped audit count == generated/acked ledger within the bounded window; without dedupe,
  duplicates > 0 at page boundaries.

### 10.4 Exactness envelope for the audit

Full-cluster exact counting is O(live rows) in time and memory. The lab report must state the
envelope: max rows scanned, memory used, wall-clock time, and whether the run completed within the
TTL. Beyond the envelope, switch to pre-aggregated ingestion counters and label the result as
approximate.

### 10.5 Artifacts

```
runs/<run_id>/
  manifest.json        # scenario, commit, environment, clock offsets
  client-<id>.ndjson   # raw samples/histograms
  summary.json         # p50/p95/p99, counters, errors
  profiles/            # pprof CPU/heap/trace (optional)
  cassandra/           # nodetool outputs, partition sizes, histograms
  report.md            # per-assumption evidence and interpretation
```

Local artifacts are the source of truth for deep analysis; `bench_*` tables are for cross-machine
visibility and the TUI.

---

## 11. Non-functional requirements

| Area | Requirement |
|---|---|
| Deployment | Single static binary; one agent per lab machine; no external services except Cassandra |
| Baseline load | 4 rooms × 10 users × 100 Hz (4k frames/s) with flat memory |
| Stress | Measured saturation curve; no artificial cap beyond resource admission control |
| Latency | Telemetry p99 within the measured envelope; claim acquisition floor ≈ Δt + 2 quorum RTTs |
| TUI | Freshness ≤ 500 ms; one `bench_latest` query per run per refresh; no DB queries per panel |
| Persistence | Run metadata/results survive agent restarts; stale runs detectable |
| Observability | Structured logs, metrics, pprof, run ids on every line |
| Reproducibility | Seeded runs; recorded environment; deterministic chaos |
| Correctness | No LWT, no DELETE on floor state, no ALLOW FILTERING in domain paths |
| Security | Lab-appropriate: localhost bind by default, optional token, validated inputs |

---

## 12. Current repository state and gaps

| Area | State |
|---|---|
| `cmd/anti-interrupt/main.go` | Good context wiring; subcommands registered |
| `internal/simulator` | Stub `Run`; flags exist |
| `internal/backend` | Stub HTTP `/`; **flags registered inside `RunE` (never parsed)**; `srv.Close` rather than graceful `Shutdown` |
| `internal/tui` | Stub |
| `internal/schema` | Bootstrap + migrate; **migration errors swallowed**; `defer Close` after query |
| `schema.cql` | Minimal telemetry + claims + turns + counters; missing naive/state/profiles/lookup/heartbeats/bench tables |
| Benchmark tables | Not implemented |
| Tests | None yet |
| Docs | `ASSUMPTIONS.md` is an early draft; this document supersedes it |

**Blocking bugs to fix first:** flag registration, migration error propagation, graceful shutdown
context, and a single migrator.

---

## 13. Decisions

| ID | Decision | Rationale |
|---|---|---|
| D1 | **Clients write directly to Cassandra; the agent is a control plane, not a proxy.** | Preserves benchmark fidelity, removes a central bottleneck, matches the "client application" requirement. |
| D2 | **No data-plane proxy.** | A proxy would absorb/reshape the load the lab is supposed to measure. |
| D3 | **Logical `turn_seq` + no DELETE on floor state.** | Defeats future-timestamp shadows and deadlocks; central to A3. |
| D4 | **`minute_bucket` is text `YYYY-MM-DDTHH:mm` UTC.** | Readable in `cqlsh`; the 16 B/row overhead is acceptable at lab scale. Revisit only if partition size/CPU demands it. |
| D5 | **Telemetry is categorical by default; rich acoustic fields are an optional migration.** | Matches the clarified payload; keeps the baseline simple while preserving the richer demo. |
| D6 | **Stats are push (client-local snapshots) + pull (domain/cluster diagnostics).** | Local metrics are exact and free; cluster state must be read from Cassandra. |
| D7 | **Snapshots and results are separate.** | 1 Hz snapshots serve the TUI; structured final summaries serve analysis (percentiles, tails). |
| D8 | **Benchmark tables live in the same keyspace with a `bench_` prefix.** | Simpler lab setup; a separate keyspace is the production-clean alternative. |
| D9 | **One gocql session per process; per-run contexts and pools.** | Cheap concurrency, no connection explosion, clean cancellation. |
| D10 | **Clock skew is simulated inside the client (`--clock-offset`).** | Reproducible and safe; avoids changing OS time. |
| D11 | **`backend` is renamed `agent`; alias retained.** | The process is a client node with a control API, not a data backend. |
| D12 | **Counters are aggregated per turn/second, never per frame.** | Per-frame counters are a hotspot and a correctness hazard. |

---

## 14. Open questions

1. **`floor_claims` retention:** add a `session_bucket` to the partition key for long-lived rooms,
   or rely on run-scoped rooms and manual cleanup? (Affects A3's turn continuity across buckets.)
2. **Heartbeats:** dedicated `floor_heartbeats` table (chosen) vs in-row TTL on the claim. Validate
   the write amplification difference under load.
3. **Audit exactness at the stress ceiling:** what is the max row budget that still completes within
   `TTL 600`? If infeasible, which approximation is acceptable for the report?
4. **Rich telemetry profile:** keep it as an optional migration, or make it the default for the
   "realistic" lab scenario?
5. **Agent authentication:** is a bearer token sufficient for the lab, or is localhost-only enough?
6. **Multi-room users:** the domain assumes a user is in one room; confirm the lookup semantics if
   a participant can join multiple rooms.
7. **Concurrent runs per agent:** default 1 for clean latency attribution; how many are needed for
   the throughput stress scenario?

---

## 15. Glossary

| Term | Meaning |
|---|---|
| **Frame** | One telemetry sample emitted by a participant at the configured frequency |
| **Turn** | A floor ownership interval identified by `turn_seq` |
| **Claim** | A contender's row for a turn |
| **Winner** | Index 0 of the ordered claim read for a turn |
| **Stabilization window (Δt)** | Wait between claim write and verification read (default 25 ms) |
| **Heartbeat** | TTL-2 s liveness row refreshed every 500 ms |
| **Torn row** | A row whose coupled columns come from different writers due to per-cell LWW |
| **Ledger** | `completed_speech_turns`: exact, idempotent turn accounting |
| **Counter drift** | Overcount caused by non-idempotent counter retries |
| **Run** | One benchmark execution identified by `run_id` |
| **Client run** | One agent's participation in a run, keyed by `(run_id, client_id)` |
| **Snapshot** | 1–5 Hz JSON state written for the TUI |
| **Observer** | Process/goroutine that pulls domain state and cluster diagnostics |
| **Auditor** | Token-range scanner with deduplication for exact counts |
| **Envelope** | The bounded range within which an exact result is feasible |
