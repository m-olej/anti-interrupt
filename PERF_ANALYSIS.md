# Performance Analysis: Anti-Interrupt Conference Call System

> **Status.** This document applies `PERF_THEORY.md` to this repository. It uses the actual
> defaults, schema, dependencies, and the hardware available on the development machine. It
> **does not modify any implementation**. Numbers marked *(model)* are analytic estimates from
> the formulas in the theory document, not measurements; every one of them has a corresponding
> experiment in §9. Treat this as a set of falsifiable hypotheses and a measurement plan, not as
> results.

---

## 1. Purpose and method

The goal is to answer four questions with evidence rather than intuition:

1. **What is the workload?** Rates, sizes, concurrency, and burstiness.
2. **Where does the latency budget go?** Which stage owns p50 and p99.
3. **What is the capacity ceiling, and which stage sets it?**
4. **Which assumptions in `ASSUMPTIONS.md` are load-bearing, which are cosmetic, and which are
   wrong in a way that will invalidate a lab-defense test?**

The method is the one from `PERF_THEORY.md` §3: define the workload → baseline → instrument →
find the saturated stage → change one variable → prove with statistics. No optimization is
recommended without a measurement that would confirm or refute it.

---

## 2. The system as it exists today

Relevant to performance, the repository currently contains:

| Component | State |
|---|---|
| `cmd/anti-interrupt/main.go` | Cobra root; one `signal.NotifyContext` root passed via `ExecuteContext`; subcommands `simulator`, `backend`, `tui` |
| `internal/simulator` | Flags (`rooms=4`, `users-per-room=10`, `freq=100`, `mode=random`), stub `Run` that blocks on `ctx.Done()` |
| `internal/backend` | Stub HTTP server (`/` only), schema bootstrap + migration, no ingestion endpoints yet |
| `internal/schema` | `BootstrapKeyspace`, `Migrate` (uses `context.Background()`, **swallows migration errors**) |
| `internal/tui` | Stub `Run` that blocks |
| `internal/backend/cmd.go` | Flags are registered **inside `RunE`**, so they are unknown at parse time and `cfg` is zero-valued; `--host/--port/--rf/--nodes` currently do not take effect |
| `cql/` | Embedded migrations |
| `docker-compose.yaml` | Cassandra 4.1 ×3, `MAX_HEAP_SIZE=128M`, `HEAP_NEWSIZE=24M`, `num_tokens=16`, `SimpleStrategy`, RF=3 |
| `schema.cql` | Four tables: `audio_telemetry`, `floor_claims`, `completed_speech_turns`, `room_call_counters` |

Two consequences for this analysis:

- **The data plane does not exist yet.** There are no telemetry/claim endpoints, no ingestion
  queue, no gocql session in the request path. The performance model below targets the design in
  `ASSUMPTIONS.md` plus the schema that is actually checked in.
- **Flag handling is broken**, which means any experiment that passes `--rooms`, `--freq`,
  `--port`, or `--rf` will silently run with defaults. This is the first thing to fix before
  benchmarking, otherwise you will be measuring the default configuration while believing you are
  measuring the stress configuration.

---

## 3. Environment

Captured from the development machine (`uname`, `go version`, `lscpu`, `free`):

| Resource | Value | Consequence for the analysis |
|---|---|---|
| CPU | AMD Ryzen 5 7530U, **6 cores / 12 threads**, up to ~4.5 GHz | 12 hardware threads shared by Go, Docker, and **3 Cassandra JVMs** |
| RAM | 22 GiB (≈11 GiB available) | 3 × 128 MB heaps + page cache + Go processes; not the constraint |
| Disk | NVMe (`/dev/nvme0n1p9`) | Sequential write path (commitlog) is fast; random read/compaction is the risk |
| Kernel | 6.18.60 (CachyOS LTS) | Modern TCP stack, cgroup v2; expect CPU frequency scaling (`scaling MHz 90%`) |
| Go | 1.27.4 | Modern GC (`GOMEMLIMIT`, `context.WithoutCancel`, PGO available) |
| Cassandra | 4.1, 3 nodes, `MAX_HEAP_SIZE=128M` | **The dominant tail-latency risk in the whole system** |

Two environment facts that will distort naive measurements:

1. **Loopback is not the lab.** On a single machine, simulator → backend → TUI traffic traverses
   loopback (tens of µs RTT, no NIC, no real packet loss). `ASSUMPTIONS.md`/`README.md` describe
   deployment across 3–4 lab machines, where RTT is 0.1–0.5 ms and clock skew is real. Any
   latency claim must be repeated with `tc netem` or on the real machines.
2. **The three Cassandra JVMs compete with Go for the same 12 threads.** CPU pinning (`taskset`)
   or a separate machine is required for a fair backend/DB split. Otherwise "Cassandra is slow"
   and "Go is slow" are indistinguishable.

### 3.1 The 128 MB heap problem

Cassandra's practical minimum heap is ~1–2 GB; 4–8 GB is typical for write-heavy nodes. 128 MB is
roughly **6–12% of the minimum**. Symptoms: frequent young GCs, large p99 pauses, slow compaction,
and `Native-Transport-Requests` queueing under even modest load. This does not mean the config is
wrong for a laptop demo — it is a deliberate "STABLE LAB" trade — but it means:

- The **first** place to look when p99 is bad is `nodetool gcstats`, `nodetool tpstats`, and
  `nodetool compactionstats`, *not* the Go process.
- Any comparison of transport or serialization formats must be run with a heap large enough that
  the JVM is not the bottleneck; otherwise the experiment has no power to detect the difference.
- The capacity numbers in §6 should be read as "ceiling with a starved JVM", not Cassandra's
  capability.

---

## 4. Workload model and arithmetic

### 4.1 Two scenarios

| Parameter | Defaults (`simulator` flags) | Stated stress target |
|---|---|---|
| Rooms | 4 | 1–10 (few rooms) |
| Users/room | 10 | up to ~500–1,000 |
| Total users | 40 | **~5,000** |
| Frequency | 100 Hz | 100 Hz |
| Events/s | **4,000** | **500,000** |

### 4.2 Payload and row size

One telemetry event carries: `room_id` (~8–16 B), `minute_bucket` (~16 B), `frame_ts` (8 B binary,
~24 B as JSON text), `user_id` (~8–16 B), `event_category` (~1–10 B).

| Representation | Estimate |
|---|---|
| JSON on the wire | **~120–180 B** (call it 150 B) |
| Cassandra row on disk/memtable | ~60–120 B including clustering metadata, cell timestamps, and overhead |
| Cells per row | 5 (2 partition key + 2 clustering + 1 regular) |

### 4.3 Derived rates

| Quantity | Defaults (4k/s) | Ceiling (500k/s) |
|---|---|---|
| Wire bytes/s | ~0.6 MB/s | ~75 MB/s |
| Logical rows/s | 4,000 | 500,000 |
| Cells/s | 20,000 | 2,500,000 |
| Replica mutations/s (RF=3) | 12,000 | **1,500,000** |
| Live rows under `TTL 600` | ~2.4 M | **~300 M** |
| Live data under TTL (at ~100 B/row) | ~240 MB | **~30 GB** |

The replica-mutation row is the important one: RF=3 means every logical write is applied to three
nodes, and `QUORUM` waits for two. At the stated ceiling the cluster must sustain **1.5 million
replica mutations per second**. That is not a laptop-class number with 128 MB heaps; the stated
stress target is aspirational, and the honest deliverable is the measured ceiling plus a curve
showing where it bends.

### 4.4 Partition sizing (the schema's real constraint)

For `audio_telemetry`, the partition key is `(room_id, minute_bucket)`, so rows per partition are
`users × frequency × 60`:

| Users/room | Rows per minute-bucket | Cells per bucket | Approx. size *(model)* |
|---|---|---|---|
| 10 | 60,000 | 300,000 | ~6–10 MB |
| 50 | 300,000 | 1,500,000 | **~30–60 MB** |
| 500 | 3,000,000 | 15,000,000 | **~300–600 MB** |

The cell-count limit (2×10⁹) is *not* the binding constraint at any of these rates — 50 users
produce 1.5×10⁶ cells per bucket, three orders of magnitude below the limit. The binding
constraint is **bytes per partition** (practical guidance ~100 MB) and the compaction/read cost
that comes with it. `ASSUMPTIONS.md` conflates the two ("wide row exhaustion" attributed to the
cell limit when the real risk is partition size). Time bucketing bounds *time*, not *size*; the
partition grows with `users × frequency`. Detailed critique in §8.

### 4.5 TTL and live-data window

`TTL 600` means the visible working set is the last ten minutes. This has three consequences:

- **Storage is bounded** at ~240 MB (defaults) / ~30 GB (ceiling) plus compaction overhead — good.
- **Audit must run inside the window.** A full-cluster audit that takes longer than 10 minutes
  reads a moving target; at the ceiling, a full scan cannot finish before the data begins to
  expire. The "count all frames" test is only meaningful at rates where the scan completes well
  within 600 s.
- **Compaction never rests.** Constant insert + constant expiry is the worst case for compaction
  backlog; expect write amplification of roughly 2–10× on top of the replica multiplier, and
  monitor `nodetool compactionstats` continuously.

---

## 5. Latency budget

Per-stage estimates *(model)* for a single telemetry event and for a floor claim:

| Stage | Telemetry | Claim | Notes / how to measure |
|---|---|---|---|
| Client batching delay | 0–50 ms (policy) | 0 | Disable for claims; telemetry tolerates it |
| HTTP hop (loopback / LAN) | 0.05 / 0.3 ms | same | `tcpdump`, client-side timing |
| HTTP parse + JSON decode (batched) | 5–20 µs/event | 5–20 µs | `pprof`, microbenchmark |
| Ingestion queue wait | 0–budget (policy) | ~0 | Instrument queue depth |
| gocql write, `QUORUM`, token-aware | 1–3 ms p50; 5–20 ms p99 | same | `nodetool proxyhistograms`, query traces |
| Claim stabilization window | — | **25 ms (fixed)** | Code constant, not a measurement |
| Claim read, `QUORUM` | — | 1–3 ms p50 | Same |
| Snapshot cache staleness (TUI) | — | — | 0–100 ms (ticker period) |
| TUI poll interval | — | — | 100–250 ms (human perception) |

**Budget conclusions:**

- **Telemetry** has no user-facing latency requirement; its budget is throughput, not response
  time. A 10–50 ms batching delay is free. End-to-end ingest p99 target of **≤ 20 ms** (excluding
  deliberate batching) is achievable if Cassandra behaves.
- **Floor acquisition** has a hard floor of ~**27–33 ms** because of the 25 ms stabilization plus
  two quorum round trips. Transport contributes <1 ms. **No transport change (HTTP, gRPC,
  WebSocket) can improve this number**; only changing the protocol (the 25 ms) can.
- **TUI freshness** is bounded below by the snapshot ticker and above by the poll interval; with
  a 100 ms ticker and a 100–250 ms poll, perceived freshness is ≤ ~350 ms, which is well inside
  human perception for a dashboard.

This is the decisive argument against WebSocket for this project: the latency floor is set by a
protocol constant and the database, not by request framing.

---

## 6. Capacity model (Little's Law)

`L = λW`, evaluated with a 3 ms average in-flight time (HTTP + quorum write):

| Scenario | λ (req/s) | W | Required in-flight |
|---|---|---|---|
| Defaults | 4,000 | 3 ms | **12** |
| Defaults, p99 20 ms | 4,000 | 20 ms | 80 |
| Ceiling | 500,000 | 3 ms | **1,500** |
| Ceiling, p99 20 ms | 500,000 | 20 ms | **10,000** |

Consequences:

- At defaults, a keep-alive pool of **16–32** HTTP connections and a bounded worker pool of ~64
  are comfortably sufficient (8× headroom over the p50 requirement; enough for p99 bursts).
- gocql is not the concurrency limit: defaults are `NumConns=2` per host with
  `MaxRequestsPerConn=32,767` (protocol v3+), i.e. up to ~65k in flight per host. The driver
  pipelines well past what this workload needs. The limit will be CPU and the DB.
- At the ceiling, **1,500–10,000 concurrent in-flight operations** are required. A goroutine per
  HTTP request is affordable in memory, but the per-request fixed costs (parsing, allocation,
  syscalls, gocql bookkeeping) become the bottleneck: Go's `net/http` costs on the order of
  10–30 µs of CPU per small request, so 500k req/s needs roughly **5–15 cores just for HTTP**,
  which exceeds the entire machine. **Batching 10× reduces this to 0.5–1.5 cores.** That is the
  single highest-leverage transport decision in the project, and it is orthogonal to WS vs HTTP.

---

## 7. Stage-by-stage analysis

### 7.1 Simulator and the Go runtime

**Good fits:** Go's goroutines and netpoller are the right model for thousands of independent
emitters. A goroutine's initial stack is ~2 KB and grows; 5,000 goroutines with modest stacks cost
tens of MB, not "a few MB", but still trivial for a 22 GB machine.

**Risks:**

- **Timer storm.** 5,000 goroutines × 100 Hz = **500,000 timer wakeups/s**. Go's timer wheel is
  efficient, but each wakeup is a scheduler event plus (in a naive design) a channel send and an
  HTTP request. Expect measurable scheduler latency and CPU (order 0.5–1 core just for wakeups),
  and expect *jitter* under load. A single scheduler goroutine that advances all participants per
  tick (batched generation) removes most of this cost and makes burstiness controllable.
- **Tick drops.** `time.Ticker` has a capacity-1 channel and **drops ticks** if the consumer is
  slow. That silently breaks the "sent events == stored events" assertion. Drops must be counted
  explicitly, or the generator must be driven by a logical clock rather than a ticker.
- **Allocation and GC.** Per-event JSON encoding allocates (encoder, buffers, strings). At
  4k/s this is negligible (~1–3 MB/s). At 500k/s it is 250–500 MB/s of allocation, which is a
  serious GC load. Mitigations that will show up in a profile: reuse encoders/buffers
  (`sync.Pool`), precompute static fields, batch, or switch to a binary format (only if measured).
- **`mode` semantics.** `races` mode deliberately synchronizes onsets; if it does so with a shared
  barrier, it creates a burst that is *worse* than random arrivals for queueing (variance term in
  Kingman's formula). Bursts must be part of the workload model, not an accident.
- **Burstiness is a design output.** The scheduler should support constant-rate (open-loop) and
  burst modes so the backend can be tested under both. A closed-loop simulator (wait for ack,
  then next tick) systematically under-reports overload latency.

### 7.2 Transport (simulator → backend, backend → TUI)

**Telemetry.** One request per frame is the wrong shape at scale: ~150 B of payload against
~150–400 B of HTTP headers means half the bytes are overhead, and the per-request CPU cost is
fixed. Batch 5–50 frames per POST with a 10–50 ms flush (size-or-time, whichever first). This:

- cuts HTTP CPU by 5–50×,
- gives the backend whole batches to write to the same `(room_id, minute_bucket)` partition,
- makes backpressure granular (one 503 per batch),
- keeps the latency added within the (nonexistent) telemetry SLO.

**Claims.** Keep these one-shot request/response and *separate from telemetry* (separate endpoint,
separate connection pool or at least separate handler path). A 25 ms stabilization window must
never sit behind a queue of telemetry writes. A WebSocket would merge both classes into one
ordered stream, creating head-of-line blocking between them — the opposite of what the protocol
needs.

**TUI.** Poll a cached snapshot at 4–10 Hz. Do **not** poll Cassandra. With a server-side snapshot
refreshed by one ticker, 1 or 100 TUIs generate identical DB load. Add `ETag`/`If-None-Match` so
unchanged snapshots return `304` with no body. At ~2 KB/snapshot and 10 Hz, a TUI costs ~20 KB/s;
this is irrelevant at any scale in this project.

**WebSocket verdict.** It saves header bytes (≈0.6–1 MB/s at defaults) and one handshake, but:

- the latency floor is the 25 ms protocol constant plus quorum round trips, not framing;
- batching is required at the ceiling regardless of protocol, and batching is easier over HTTP;
- WS requires you to build acks, backpressure, reconnect/resume, and correlation IDs — none of
  which HTTP leaves you without;
- a single long-lived stream creates HOL coupling between telemetry and claims;
- it adds a dependency and removes `curl` from the debugging toolbox.

If push to the TUI is ever needed, **SSE** is the natural step (one-way, plain HTTP, automatic
reconnect). WS only becomes justified if you measure transport as the bottleneck *and* need
bidirectional low-latency messaging — neither is true here.

**HTTP client configuration that matters more than protocol choice:**

- One shared `http.Client`; `MaxIdleConnsPerHost` explicitly set (the default of **2** causes
  connection churn with many emitters); `IdleConnTimeout` set; `DisableCompression` for tiny
  payloads (gzip on 150 B is a net loss); drain and close every response body so connections
  return to the pool.
- Bound in-flight requests (semaphore/`errgroup.SetLimit`) instead of one goroutine per event.
- Per-request `context.WithTimeout` (e.g. 250 ms–1 s) — the driver's default query timeout is
  **11 s**, which is 20–400× any sensible SLO and will pin goroutines during an incident.
- Retries only for idempotent events, with capped exponential backoff + jitter.

**HTTP/2 (h2c)** is a possible later step if many small concurrent requests per connection are
measured to be expensive. It needs `golang.org/x/net/http2/h2c`, and its default
`MaxConcurrentStreams=250` and per-stream buffers must be tuned. Not a first move.

### 7.3 Backend ingestion and the database driver

The backend is the write amplifier: every simulator event becomes one or more Cassandra mutations.
Design points that dominate performance:

- **Bounded ingestion queue + worker pool.** Accept from HTTP, enqueue into a bounded channel,
  return `202`/`204` on acceptance, `503` + `Retry-After` when full. This converts overload into a
  measurable rejection rate instead of unbounded memory. Without it, `net/http`'s
  goroutine-per-request plus gocql's 65k in-flight capacity will happily buffer the entire
  simulator in RAM.
- **Split latency classes.** Telemetry can be batched and delayed; claims cannot. Use separate
  worker pools/queues (bulkheads) so a compaction stall or a telemetry backlog cannot delay the
  25 ms claim window.
- **Token-aware routing.** The current `gocql.NewCluster(nodes...)` does **not** set
  `TokenAwareHostPolicy`. Without it, writes go to an arbitrary coordinator and then to replicas —
  an extra network hop per write and more coordinator CPU. Enabling token awareness is likely a
  larger win than any serialization change. (Scylla's shard-aware port is a further step if using
  Scylla.)
- **Prepared statements.** Current CQL is built with `fmt.Sprintf` per call, so every query is
  parsed and planned again, and identifiers are injectable. Prepared statements cut server CPU and
  remove the injection surface (and the `--rf` flag is formatted directly into DDL — see §8.11).
- **`WriteCoalesceWaitTime`** defaults to 200 µs: gocql already batches small writes to a
  connection with `writev`. This is a built-in transport optimization; do not disable it, and
  measure its effect by toggling it, not by assuming.
- **Consistency level.** `QUORUM` write + `QUORUM` read on RF=3 gives read-your-writes. `ONE` is
  faster and weaker. Choose per operation: telemetry can plausibly be `ONE` (loss-tolerant,
  upsert-idempotent), claims need `QUORUM`. Mixing `ONE` telemetry with `QUORUM` claims is a
  deliberate latency/durability trade, not an oversight — but it must be documented, because it
  changes the "no loss" test's meaning.
- **Idempotency.** Telemetry upserts keyed by `(room_id, minute_bucket, frame_ts, user_id)` are
  idempotent, so retries are safe. Counter increments are not. This is the correct foundation for
  a retry policy; it must be reflected in `Idempotent(true/false)` per query type.
- **Deadline propagation.** Pass a per-request context into the write (`QueryWithContext`), so a
  client disconnect or a deadline cancels the DB work instead of letting it run for 11 seconds.

### 7.4 Cassandra schema and write path

The schema is the performance center of gravity. Per-table analysis:

#### `audio_telemetry`

**Strengths.** The query-driven design is right: `PRIMARY KEY ((room_id, minute_bucket),
frame_ts, user_id)` serves "frames for a room in a time window" with a single-partition slice,
and `CLUSTERING ORDER BY (frame_ts DESC, user_id ASC)` matches the dashboard's "latest first"
needs. TTL avoids client deletes and tombstones from deletion.

**Problems.**

1. **Partition size is rate-dependent.** As computed in §4.4, 500 users/room at 100 Hz produces
   ~300–600 MB per minute-bucket. That breaks the ~100 MB guidance long before the 2×10⁹ cell
   limit. The fix is to bound by *size*, not only time: add a hash shard to the key (e.g.
   `(room_id, minute_bucket, shard)` where `shard = hash(user_id) % K`) or use shorter buckets at
   high rates. The trade: reads that need all users must fan out over `K` partitions. For this
   project, K=1 is fine at 4 rooms × 10 users; it becomes a scaling cliff at the stated ceiling.
2. **`minute_bucket` is `text`.** It is repeated in every row's partition key, consuming ~16 B per
   row; at 300 M live rows that is ~5 GB of pure duplication. An `int` epoch-minute or a
   truncated `timestamp` is cheaper and avoids format bugs. Minor at demo scale, real at scale.
3. **Duplicate timestamps overwrite.** `frame_ts` (microsecond) + `user_id` is the clustering key.
   If a generator ever emits two frames for the same user in the same microsecond — or if two
   machines' clocks disagree and collide — the row is an upsert and one event disappears. At
   100 Hz this is unlikely but not impossible, and it silently violates "sent == stored". A
   per-event unique ID (or a guaranteed-unique sequence) makes the assertion provable.
4. **TTL vs compaction.** `TTL 600` with a sustained high write rate keeps compaction permanently
   busy. Monitor `nodetool compactionstats`; a growing `pending tasks` count means read latency
   will rise regardless of Go-side tuning.

#### `floor_claims`

**Strengths.** No LWT, deterministic ordering via clustering columns, and "read the top row" is
an efficient single-partition query. The append-only claim log is the right idea.

**Problems.**

1. **Unbounded partition growth.** The key is `(room_id)` with no TTL and a monotonic `turn_seq`.
   Every claim for the life of the key accumulates in one partition. Claims are self-limited by
   the 25 ms window (~40/s/room), so a 1-hour demo is ~144k rows — survivable — but the table has
   no retention policy and will grow across sessions. `ASSUMPTIONS.md` proposed an `epoch_bucket`
   in the partition key; `schema.cql` dropped it. That inconsistency matters for long-running
   keyspaces.
2. **Single-partition hotspot.** All claims for a room hit one replica set. At 40/s this is fine;
   if the 25 ms window is removed or many rooms share a node, the coordinator and the owning
   replicas become the serialization point. The rate ceiling per room is `1/25ms = 40 claims/s`
   by construction — an intentional cap, but one that should be stated as a design limit, not
   discovered.
3. **`claim_status` mutation.** Status transitions (`CLAIMING → SPEAKING → RELEASED`) rewrite rows
   with new cell timestamps, creating versions until compaction. Low volume, but it means the
   table is not purely append-only in practice.

#### `completed_speech_turns`

Keyed by `(room_id, user_id), turn_id`; one partition per user per room, growing for the session
(~1 turn/s → 3,600 rows/hour). Fine at demo scale. Without a `session_id` in the key or a TTL,
history accumulates across sessions and partitions grow without bound over the project's life.
The idempotent upsert (`turn_id` is a UUID) is exactly the right pattern for retries.

#### `room_call_counters`

This is the most dangerous table for performance.

- Counter writes are **read-modify-write per replica** and **non-idempotent**; they cannot be
  retried blindly, cannot be batched with normal mutations, and maintain a counter cache that is
  expensive to rebuild.
- The name `speech_frames_count` suggests a per-frame increment. At 100 Hz per user that is
  **5,000 counter increments/s at defaults and 500,000/s at the ceiling**, each touching three
  replicas — a self-inflicted hotspot and a correctness hazard on timeout.
- The right shape is to **aggregate in the backend** (per turn, per second, or per batch) and
  write one counter mutation per aggregate. If exact per-frame counts are required for the lab,
  derive them from the telemetry rows (or a per-batch static column) rather than a counter.
- `ASSUMPTIONS.md`'s dual-track design (counters at `ONE` + idempotent turn log) is sound in
  principle; the risk is the update *granularity*, which the current schema does not constrain.

#### Cross-cutting storage issues

- **RF=3 on 3 nodes** means 100% of data on every node and every `QUORUM` operation touching two
  replicas. It is a deliberate fault-tolerance choice (tolerates one node down) but removes write
  locality and capacity headroom: the cluster's throughput is the *slowest* node's throughput.
- **`SimpleStrategy`** is acceptable for a single-DC lab; `NetworkTopologyStrategy` is the
  production answer and is required if a second DC is ever added. Changing RF later requires
  `ALTER KEYSPACE`; `CREATE KEYSPACE IF NOT EXISTS` silently ignores a changed `--rf` on an
  existing cluster, which will confuse a lab test that expects a new RF.
- **Commitlog + memtable + compaction** is the real write path: acknowledgment happens after the
  commitlog append and memtable update, not after an SSTable flush. Commitlog sync is batched
  (group commit), which is why Cassandra sustains high write rates on one disk; the cost appears
  later as compaction. This is the correct mental model for interpreting write latency: fast
  acks, deferred cost.

### 7.5 Auditor

`ASSUMPTIONS.md` describes a full-ring token scan with client-side deduplication for an exact
global count. The cost model:

| Scenario | Live rows (600 s TTL) | Dedup set memory *(model)* | Feasibility |
|---|---|---|---|
| Defaults (4k/s) | 2.4 M | ~100–300 MB | Slow but possible (tens of seconds to minutes) |
| Moderate (50k/s) | 30 M | ~1.5–4 GB | Marginal |
| Ceiling (500k/s) | **300 M** | **~15–40 GB** | Infeasible on this machine, and the scan outlives the TTL |

A full scan also competes with compaction and normal traffic, so it *causes* the latency it is
trying to measure. Exact global counts over high-rate TTL data need either:

- **pre-aggregation at ingestion** (count accepted events in the backend, per batch/per minute —
  this is also the natural place to assert "sent == stored"), or
- **approximate counting** (HyperLogLog with a known error bound) if exactness is not actually
  required, or
- a **bounded audit window** with a documented sampling/estimation method.

The requirement ("exact cluster-wide totals") and the mechanism (full scan + in-memory dedupe) are
incompatible at the stated ceiling. This should be resolved in the design, not discovered during
the demo.

### 7.6 TUI

- Poll 4–10 Hz; serve from a precomputed snapshot; add `ETag`/`If-None-Match`; never query
  Cassandra per poll. Cost: ~20 KB/s per client, zero DB load.
- Decouple three rates: data refresh (4–10 Hz), UI render (30–60 Hz), and user input. Tying render
  to data forces either too much DB work or a sluggish UI.
- If the arbiter state is authoritative in the backend's memory, the snapshot is a copy; make that
  copy explicit (struct copy or double-buffer) to avoid data races between the writer goroutine
  and the HTTP handlers. `-race` will catch it; design for it.

---

## 8. Critique of the assumptions

Each row: the claim as written, the performance reality, and the experiment or design change that
would resolve it. "Impact" is about the validity of the lab defense, not just speed.

### 8.1 "5,000 goroutines with `time.Ticker` … near-zero scheduling jitter"

**Reality.** 5,000 × 100 Hz = 500k timer wakeups/s. Go's timer and scheduler are good, but
"near-zero jitter" is not a guarantee, especially on 12 threads shared with three JVMs. Tickers
also *drop* ticks under backpressure, which silently breaks event counting.

**Impact.** Medium: the stress test may report lower rates than intended, or "lost" events that
were never generated. **Resolve:** count generated vs dropped ticks; compare per-goroutine tickers
against a single batched scheduler tick; report scheduler latency (Go trace).

### 8.2 "5,000 goroutines … in a few megabytes of RAM"

**Reality.** Initial goroutine stack ~2 KB, but realistic stacks grow to 4–8 KB+ with call depth
and I/O. 5,000 × ~8 KB ≈ 40 MB, plus per-goroutine buffers/clients. Still cheap, but off by ~10×.

**Impact.** Low (correctness of the budget claim, not the design). **Resolve:** measure RSS at
5,000 goroutines; it is a one-line experiment.

### 8.3 "A 50-person call at 100 Hz writing 5 columns generates 25,000 cells/s … 90,000,000
cells/hour … eventually crashes the node"

**Reality.** The arithmetic is right (5,000 frames/s × 5 cells = 25,000 cells/s; ×3,600 =
90M cells/hour), but the causal claim targets the wrong limit. 90M cells/hour is nowhere near
2×10⁹ cells *per partition*; and 300k rows/minute-bucket is ~1.5M cells — three orders of
magnitude below the hard limit. The real risk is **bytes per partition** (~30–60 MB at 50 users,
~300–600 MB at 500 users) and the compaction/read cost, not cell exhaustion.

**Impact.** High for the report's credibility: the stated failure mode is not the one that occurs.
**Resolve:** measure partition sizes with `nodetool tablestats` at increasing user counts; state
the 100 MB guidance and the rate-dependent bound.

### 8.4 "Composite time-bucketed partition key bounds each partition to exactly 60 seconds"

**Reality.** It bounds *time*, not *size*. Size = `users × frequency × 60 s × bytes/row`. Adding
users or raising frequency grows the partition linearly while the bucket duration stays fixed.

**Impact.** High at scale, low at defaults. **Resolve:** define the target partition size, derive
the max users/bucket from it, and shard or shorten buckets when that is exceeded.

### 8.5 "Optimistic verification: write at QUORUM, wait Δt = 25 ms, read at QUORUM"

**Reality.** On a healthy 3-node LAN, a `QUORUM` write acks in ~1–3 ms, and `W + R > R` already
guarantees read-your-writes for `QUORUM`+`QUORUM`. The 25 ms is 8–25× the actual write latency; it
is insurance against in-flight replication and clock skew, not a requirement of the consistency
model. It also caps claims at 40/s/room and adds a fixed 25 ms to every acquisition.

**Worse:** `claim_ts` is a client-supplied microsecond timestamp. If simulators stamp claims
locally, cross-machine clock skew (typically sub-ms with NTP, but ms during anomalies) can reorder
concurrent claims regardless of the 25 ms wait. The wait does not fix skew; it only reduces the
probability of reading before replication.

**Impact.** High on latency and throughput; the mechanism is a correctness crutch. **Resolve:**
since all simulator traffic goes through the backend (per the program structure), the backend can
assign a single monotonic timestamp — eliminating skew entirely — and the wait can be reduced to
the measured replication lag (or zero under `QUORUM`+`QUORUM`). Validate with a race test at
2–15 ms onset spread and a clock-skew injection test.

### 8.6 "Simulator … doesn't interact with Cassandra" vs. "client writes its claim … reads back"

**Reality.** These statements contradict each other. If the simulator never touches Cassandra,
then the arbiter (backend) executes the write-wait-read protocol on the client's behalf, and the
"client" in §"Optimistic Verification Protocol" is the backend. If the simulator does talk to
Cassandra, the whole transport/architecture discussion changes and the backend is bypassed for
claims. The current code (simulator has no Cassandra dependency; backend owns `schema` and the
future data plane) implies the former.

**Impact.** High: the ownership of the protocol determines where the latency and the retry logic
live. **Resolve:** state explicitly that the backend is the sole Cassandra client, and rewrite the
protocol description with "backend" as the actor.

### 8.7 "Counters at `ConsistencyLevel.ONE` with `Idempotent(false)`"

**Reality.** Correct for safety, but the granularity is the problem. Per-frame increments are a
hotspot; per-turn increments are fine. Counter mutations also cannot be blended into logged
batches with normal writes, and their commitlog path is special.

**Impact.** High if implemented per frame. **Resolve:** define the aggregation window (per turn /
per second / per batch), assert the increment count in a test, and measure counter write latency
separately from normal writes.

### 8.8 "`list<double>` of last 10 noise floors in `SpeakerProfiles`"

**Reality.** A collection update is a read-modify-write of the whole cell. Concurrent
calibrations for the same user serialize and can overwrite each other; a 10-element list is
rewritten on every update. At 100 Hz this is 100 rewrites/s of the same cell per user.

**Impact.** Medium. **Resolve:** a separate table keyed by `(room_id, user_id, ts)` with TTL and
`LIMIT 10` on read, or keep the history in memory and persist at a low rate.

### 8.9 "Token-range paginator + client-side deduplication for exact cluster-wide totals"

**Reality.** O(live rows) memory and O(cluster) I/O; at the ceiling, ~300M rows and tens of GB of
dedupe set, and the scan outlives the TTL. It also perturbs the system it measures.

**Impact.** High: the test cannot complete at the advertised load. **Resolve:** define "exact"
precisely; prefer ingestion-time counters or per-bucket aggregates; if exact post-hoc counts are
required, cap the test rate and window, and document the bound.

### 8.10 "Writes at QUORUM … automatically retry on `WriteTimeoutException`"

**Reality.** Idempotent upserts can be retried; counters cannot. Retrying during an overload
amplifies the overload (retry storm), and the driver's default 11 s timeout means failed requests
occupy resources for a long time.

**Impact.** High during failure injection. **Resolve:** per-query idempotency flags, capped
retries with jitter, short per-request deadlines, and a circuit breaker/load shedder. Measure the
retry amplification factor under a 1-node-down test.

### 8.11 Non-performance footnotes that will invalidate measurements

- **Flags registered inside `RunE`** (`internal/backend/cmd.go`): they are never parsed, so every
  benchmark runs defaults. Fix before any experiment.
- **`schema.Migrate` swallows errors** (logs and returns `nil`, and `migrate.Pending` failure is
  ignored): a benchmark can run against a half-created schema and report meaningless numbers.
- **`Schema.BootstrapKeyspace` formats `keyspace` and `rf` directly into CQL**: an injection
  surface and a source of silent misconfiguration (`CREATE IF NOT EXISTS` ignores a changed RF).
- **`defer boot.Close()` is registered after the query**: a failed bootstrap leaks the session.
- **`ASSUMPTIONS.md`'s `floor_claims` DDL is invalid**: `priority_tier` appears in the primary key
  while its column definition is commented out; the prose, the assumptions DDL, and `schema.cql`
  give three different keys. The checked-in `schema.cql` is the only one that parses; treat it as
  canonical and fix the documents.
- **The assumptions' PK `((room_id, epoch_bucket), priority_tier, claim_ts, user_id)` differs from
  `schema.cql`'s `((room_id), turn_seq, priority_tier, claim_ts, user_id)`** — the former bounds
  partition growth, the latter does not. Decide which property you are defending.

### 8.12 What the assumptions get right

To be fair, and because a critique that only lists faults is useless:

- **Query-driven schema** with `(room_id, minute_bucket)` and `(room_id, user_id)` partitions is
  the correct Cassandra modeling instinct.
- **TTL instead of deletes** avoids tombstone floods — correct.
- **Idempotent turn log with UUIDs** is the right retry foundation.
- **Avoiding LWT** and replacing it with a deterministic append-only ordering is a legitimate and
  interesting design; the cost is the stabilization window, which should be minimized, not
  eliminated casually.
- **Batching at the driver level** already exists (`WriteCoalesceWaitTime`) and tokens are
  available for locality; the design simply needs to turn them on.
- **Go as the language** is a good fit: cheap concurrency, predictable deployment, first-class
  profiling/tracing, and a mature Cassandra driver.

---

## 9. Experiment plan

All commands assume the repo root. Run experiments in the order below; each has a hypothesis and
an acceptance criterion. Store raw outputs as artifacts (commit hash + environment).

### Phase 0 — Environment and DB baseline (before app code)

**Hypothesis:** with 128 MB heaps, Cassandra p99 is dominated by GC/compaction, not by network or
Go.

```bash
docker compose up -d --wait
docker stats --no-stream
docker exec cass1 nodetool status
docker exec cass1 nodetool gcstats
docker exec cass1 nodetool tpstats
cassandra-stress write n=200000 -rate threads=32 -node 127.0.0.1
cassandra-stress mixed ratio\(write=9,read=1\) n=200000 -rate threads=32 -node 127.0.0.1
```

Then repeat with `MAX_HEAP_SIZE=1G`/`HEAP_NEWSIZE=256M` in a separate compose profile (do not
change the default file for the demo). **Acceptance:** quantify the p99 difference attributable to
heap size. If the difference is large, no application-level tuning is meaningful until the heap is
addressed.

### Phase 1 — Microbenchmarks (no network, no DB)

- JSON encode/decode of one event and of a batch of 10/50 (`testing.B` + `-benchmem`).
- HTTP handler with a no-op body vs. one that enqueues.
- gocql write path (prepared vs. `Sprintf`, token-aware vs. round-robin) against a single node.

```bash
go test -run '^$' -bench . -benchmem -count=10 ./internal/... | tee bench-old.txt
benchstat bench-old.txt bench-new.txt
go build -gcflags='-m=2' ./... 2>&1 | grep escapes
```

**Acceptance:** know the per-event CPU and allocation cost of each layer; establish which layer
would dominate at 500k/s.

### Phase 2 — Ingest ceiling (no Cassandra)

Run the backend with the queue but a no-op writer. Open-loop load, increasing rate until p99
breaks the budget.

```bash
wrk2 -t4 -c64 -R4000  -d60s --latency -s post.lua http://127.0.0.1:1234/v1/telemetry
wrk2 -t4 -c64 -R20000 -d60s --latency -s post.lua http://127.0.0.1:1234/v1/telemetry
hey -c 64 -z 30s -m POST -T application/json -d @batch.json http://127.0.0.1:1234/v1/telemetry
```

**Acceptance:** the HTTP+queue layer sustains ≥ 10× the DB ceiling with p99 < 20 ms; identify the
knee (open-loop, not closed-loop).

### Phase 3 — End-to-end with Cassandra

Simulator → backend → Cassandra, with counters for generated/sent/accepted/rejected/stored, and
`nodetool` histograms sampled during the run.

```bash
# terminal 1
./anti-interrupt backend
# terminal 2
./anti-interrupt simulator --rooms 4 --users-per-room 10 --freq 100
# terminal 3 (during the run)
watch -n2 'docker exec cass1 nodetool tpstats | head -30'
docker exec cass1 nodetool proxyhistograms
docker exec cass1 nodetool tablestats anti_interrupt.audio_telemetry
```

**Acceptance:** flat memory, zero unexplained drops, p99 within the budget, and a documented
saturation curve (rate → p99 → rejects). Then repeat with `tc netem` on the loopback interface to
simulate the 3–4 machine deployment.

### Phase 4 — Failure injection

- `docker stop cass2` during a run: measure error rate, retry amplification, recovery time,
  whether `QUORUM` still succeeds (it should, with RF=3).
- `tc qdisc add dev lo root netem delay 5ms loss 0.1%` (and `tc qdisc del ...` to reset).
- Skew the simulator's clock (if a `chaos-clock` mode is implemented) and verify claim ordering.
- Pause the backend (`SIGSTOP`) for 2 s and observe client timeouts/retries.

**Acceptance:** no unbounded queues, bounded retry amplification, graceful recovery, and a written
description of the failure semantics.

### Phase 5 — TUI and snapshot

Simulate N TUI clients polling at 4–10 Hz; measure server CPU and DB reads (should be zero for
snapshots).

```bash
for i in $(seq 1 50); do curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:1234/v1/rooms/r1/snapshot & done
```

**Acceptance:** server CPU scales sub-linearly with clients; `304` rate reflects unchanged
snapshots.

### Profiling during the above

```bash
GODEBUG=gctrace=1 ./anti-interrupt backend
go tool pprof -http=:8081 http://127.0.0.1:1234/debug/pprof/profile?seconds=30
go tool pprof -http=:8081 http://127.0.0.1:1234/debug/pprof/heap
go tool trace -http=:8082 trace.out
perf stat -d -p $(pgrep -f 'anti-interrupt backend')
ss -tinp | head -40
```

---

## 10. Prioritized recommendations

Ordered by expected impact per unit of effort. None of these is implemented here.

### P0 — Required before any number can be trusted

1. **Fix flag registration and migration error handling.** Otherwise every experiment runs
   defaults and a failed migration is invisible.
2. **Measure the Cassandra baseline with an adequate heap.** Record p50/p99 at 128 MB and at
   1–2 GB; use the difference to decide whether app tuning is even measurable.
3. **Batch telemetry at the client** (5–50 frames, 10–50 ms flush). Expected: 5–50× less HTTP CPU
   and fewer syscalls; no telemetry SLO to violate.
4. **Separate claim traffic from telemetry** (separate endpoint and worker pool). Expected:
   the 25 ms claim window stops depending on the telemetry backlog.
5. **Bounded ingestion queue + explicit backpressure** (`503`/`429` when full) and **per-request
   deadlines** (sub-second, not the 11 s driver default). Expected: overload becomes a measured
   rejection rate instead of memory growth and latency collapse.
6. **Token-aware routing + prepared statements + per-query idempotency flags.** Expected: one
   fewer network hop per write, lower DB CPU, safe retries.

### P1 — High impact once the basics are measured

7. **Counter aggregation.** Never increment per frame; define the window and test the count.
8. **TUI snapshot cache + ETag**, 4–10 Hz. Expected: near-zero DB load from TUIs.
9. **Bound partition size.** Derive max users/bucket from a target size; shard or shorten buckets
   when exceeded. Fix the schema/assumption inconsistency and add retention to `floor_claims` /
   `completed_speech_turns`.
10. **Replace the full-scan audit** with ingestion-time aggregation or a bounded/approximate
    method; state the exactness requirement.
11. **Re-examine the 25 ms stabilization.** Measure actual replication lag; consider
    backend-assigned monotonic timestamps to remove clock skew, then reduce the wait.

### P2 — Only if the profile justifies it

12. **Serialization format.** Switch to Protobuf/binary only if JSON shows up as a measured
    fraction of CPU at the target rate.
13. **HTTP/2 (h2c) or WS.** Only if transport CPU/headers are measured as the bottleneck. SSE if
    TUI push is required.
14. **Go tuning:** `GOMEMLIMIT`, buffer pools, PGO (`-pgo`), CPU pinning for benchmarks.
15. **Cassandra tuning:** larger heap, `NetworkTopologyStrategy`, more nodes, compaction
    throttling — only after the schema and access patterns are settled.

---

## 11. What not to optimize

- **Do not add WebSocket.** The latency floor is the 25 ms protocol constant and quorum RTT; the
  throughput ceiling is Cassandra. WS would add ack/backpressure/reconnect complexity and HOL
  coupling for a saving that batching already provides.
- **Do not micro-optimize JSON before measuring.** At defaults it is <1% of a core; at the ceiling
  batching dominates.
- **Do not spawn 5,000 tickers if one scheduler tick can generate the workload.** Measure both,
  but expect the batched scheduler to win on jitter and CPU.
- **Do not tune Go's GC while the 128 MB Cassandra heap is the bottleneck.**
- **Do not claim "no loss" without counting at three points:** generated, accepted, and stored.
  Ticker drops, HTTP retries, and TTL expiry can each break the equality.
- **Do not benchmark only on loopback and extrapolate to 3–4 machines.** Use `tc netem`.
- **Do not trust a single run.** Warm up, repeat, and report confidence intervals.

---

## 12. Open questions that only measurement can answer

1. What is the actual sustained logical write rate of the 3-node cluster at 128 MB vs 1 GB heap,
   at `ONE` and `QUORUM`?
2. What fraction of end-to-end p99 is Cassandra, HTTP, JSON, and the queue, at 4k/s and at the
   measured ceiling?
3. How much does token-aware routing save per write on this cluster?
4. How much does batching (10×, 50×) reduce CPU per event, and at what added latency?
5. Does the 25 ms window matter for correctness on a healthy LAN, and what is the actual
   read-after-write consistency without it?
6. How many users/room can one minute-bucket hold before `nodetool tablestats` exceeds ~100 MB?
7. What is the audit's wall-clock time and memory at 4k/s, 50k/s, and the measured ceiling?
8. How does the system behave with one node down: error rate, retry amplification, recovery time?

---

## Appendix A — Formulas used

```
Little's Law:            L = λW
Utilization:             ρ = λ/µ
M/M/1 time in system:    W = S / (1 − ρ)
Kingman's approximation: E[Wq] ≈ (ρ/(1−ρ)) · ((Ca² + Cs²)/2) · E[S]
Amdahl:                  S(N) = 1 / ((1−p) + p/N)
USL:                     C(N) = N / (1 + σ(N−1) + κN(N−1))
Roofline:                P = min(π, β·I)
BDP:                     bytes_in_flight = bandwidth · RTT
Replica mutations:       logical_writes × RF
Partition rows:          users × frequency × bucket_seconds
Live rows:               event_rate × TTL_seconds
Dedup set bytes:         live_rows × bytes_per_key
```

## Appendix B — Environment capture (for reproducibility)

```bash
go version
uname -a
lscpu | grep -E 'Model name|^CPU\(s\)|Thread|Core|Socket|MHz'
free -h
docker version --format '{{.Server.Version}}'
docker compose config
```

Record these with every benchmark result. A performance number without its environment is an
anecdote.

## Appendix C — Quick command reference used above

```bash
# Go
go test -run '^$' -bench . -benchmem -count=10 ./...
benchstat a.txt b.txt
go tool pprof -http=:8080 cpu.out
go tool trace trace.out
GODEBUG=gctrace=1 ./anti-interrupt backend

# Load (open loop is authoritative for tails)
wrk2 -t4 -c64 -R4000 -d60s --latency http://host/path
vegeta attack -rate=4000 -duration=60s -targets targets.txt | vegeta report
hey -c 64 -z 30s -m POST -T application/json -d @body.json http://host/path

# Cassandra
cassandra-stress write n=200000 -rate threads=32
nodetool tpstats
nodetool proxyhistograms
nodetool tablehistograms anti_interrupt.audio_telemetry
nodetool compactionstats
nodetool gcstats
nodetool tablestats anti_interrupt.audio_telemetry

# System
perf stat -d -p <pid>
ss -tinp
iostat -x 1
vmstat 1
tc qdisc add dev lo root netem delay 5ms loss 0.1%
```