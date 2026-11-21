# Performance Engineering: Theory and Practice

> **Scope.** This document is the conceptual half of a two-part report. It explains how a
> performance-focused engineer reasons about a system: the metrics, the laws, the cost model of
> the stack, the architectural patterns, and the tools used to measure all of it. The companion
> document, `PERF_ANALYSIS.md`, applies this material to the Anti-Interrupt project and critiques
> its assumptions. No implementation is changed by either document.

---

## 0. The one-paragraph version

Performance engineering is **applied queueing theory plus measurement discipline**. Every system
is a network of queues (CPU run queue, network socket, connection pool, disk, database). Each has
a service rate; traffic arrives at some rate. As utilization approaches 1, waiting time grows
non-linearly, and the *tail* (p95/p99), not the mean, becomes what users and timeouts experience.
Your job is to (1) define a workload and a latency/throughput target, (2) find the stage with the
highest utilization — the bottleneck — using measurements rather than intuition, (3) reduce work,
add parallelism, or batch/coalesce at that stage, and (4) prove the change with statistically
sound before/after numbers. Everything else — language, protocol, batching, pooling, caching — is
a tool applied to step 3.

---

## 1. What "performance" means: the four axes

### 1.1 The axes

| Axis | Question | Typical metric |
|---|---|---|
| **Latency** | How long does one operation take? | p50, p95, p99, max (seconds) |
| **Throughput** | How many operations per unit time? | req/s, events/s, MB/s, writes/s |
| **Resource use** | What does it cost to sustain that? | CPU %, RSS, IOPS, bytes/op, $/M req |
| **Stability** | Does it hold under overload? | error rate, queue depth, p99 drift, OOM |

These trade against each other. Batching raises throughput and *adds* latency. More concurrency
raises throughput until a shared resource saturates, then it *raises* latency and error rate.
Caching trades memory and freshness for latency. A performance decision is always "optimize which
axis, subject to what constraint on the others".

### 1.2 Service time, response time, waiting time

- **Service time (S):** time the resource actually works on the request.
- **Waiting time (Wq):** time spent queued before service.
- **Response time (R = Wq + S):** what the client observes.

Most "slow system" bugs are *waiting* time, not service time: lock contention, connection pool
exhaustion, GC pauses, a saturated disk, coordination with a slow replica. Optimizing service
time when the queue is the problem produces no visible improvement. This distinction is the
single most useful diagnostic question in the field: **is it working longer, or waiting longer?**

### 1.3 Averages lie; tails matter

A mean of 10 ms can hide a bimodal distribution of 1 ms and 1 s. In distributed systems the
client's p99 is often assembled from many servers' p99s, and requests that touch *N* services
hit the slow tail of at least one with probability `1 - (1 - p)^N`. At p = 1% and N = 100
fan-out, ~63% of requests hit a tail event. Report percentiles and the distribution shape, not
the mean. Also beware **coordinated omission**: a closed-loop load generator that waits for a
response before issuing the next request *misses* the latency of requests that would have been
sent while the system was stalled, under-reporting the tail. Tail measurement needs an open-loop
generator with a fixed arrival rate (see §8.3).

### 1.4 SLOs, SLIs, and latency budgets

An **SLI** is a measured indicator (p99 latency, error rate). An **SLO** is a target on it
("p99 < 50 ms, 99.9% success"). A **latency budget** divides the SLO across stages:
`budget_total ≥ budget_network + budget_queue + budget_decode + budget_db + budget_encode`.
Budgets turn vague goals into design constraints: if the total is 50 ms and the database's
measured p99 is 30 ms, you have 20 ms for everything else and a mandate to keep the transport
cheap. Write the budget down before choosing technologies.

---

## 2. The laws and models you must know

### 2.1 Little's Law — the most useful formula in the field

```
L = λ × W
```

`L` = average number of items in the system, `λ` = arrival rate, `W` = average time in system.
It is exact for any stable system, needs no distribution assumptions, and applies to connections,
threads, in-flight requests, queue depth — anything.

**Uses:**
- *Pool sizing:* to sustain 4,000 req/s with 2 ms average latency you need `L = 4000 × 0.002 = 8`
  requests in flight. A pool/worker count well below 8 caps throughput; far above 8 just adds
  queueing.
- *Checking a claim:* "Our 64-connection pool handles 100k req/s" implies `W = L/λ = 0.64 ms`
  average. If measured latency is 5 ms, the real ceiling is `64 / 0.005 = 12.8k req/s`.
- *Diagnosing:* if measured queue depth is much higher than `λW`, work is leaking (leaked
  connections, stuck goroutines) or arrivals are bursty.

### 2.2 Utilization and queueing delay

For a single server with arrival rate `λ` and service rate `µ`, utilization is `ρ = λ/µ`.
The M/M/1 model gives average time in system:

```
W = 1 / (µ − λ) = S / (1 − ρ)
```

so waiting time explodes as `ρ → 1`:

| Utilization ρ | Queue wait in units of service time |
|---|---|
| 50% | 1 |
| 70% | 2.3 |
| 80% | 4 |
| 90% | 9 |
| 95% | 19 |
| 99% | 99 |

This is why engineers keep production utilization around 60–70%: at 90% a 1 ms service time
becomes ~9 ms of waiting, and any burst tips the system into unbounded queues. The precise curve
depends on variability — **Kingman's approximation**:

```
E[Wq] ≈ (ρ / (1 − ρ)) × ((Ca² + Cs²) / 2) × E[S]
```

where `Ca²` and `Cs²` are the squared coefficients of variation of arrivals and service. **Less
variability means shorter queues at the same utilization.** Batching, rate limiting, and
connection reuse work partly because they reduce variance; bursty arrival patterns (a ticker
firing 5,000 goroutines in the same millisecond) inflate it.

### 2.3 Amdahl's Law and the Universal Scalability Law

Amdahl: if a fraction `p` of the work is parallelizable, speedup with `N` workers is
`S(N) = 1 / ((1 − p) + p/N)`. A 5% serial section caps speedup at 20× no matter how many cores.

The Universal Scalability Law adds **coherency** (crosstalk, contention, coordination):

```
C(N) = N / (1 + σ(N − 1) + κN(N − 1))
```

where `σ` is serialization and `κ` is coherency. With non-zero `κ`, throughput *decreases* past
a peak as you add workers. This is the theory behind "more goroutines made it slower": shared
locks, cache-line ping-pong, or a single database coordinator. It also predicts that scaling is
not free, and that measuring the peak is more useful than assuming linear scaling.

### 2.4 The bottleneck / theory of constraints

At any moment, throughput is limited by exactly one stage — the constraint. Improving any other
stage does not change throughput (it may lower latency in that stage, which is still useful, but
it will not raise the ceiling). The method is therefore:

1. Identify the constraint by measuring utilization and queue depth per stage.
2. Exploit it (reduce work, batch, cache, avoid retries).
3. Subordinate everything else to it (don't over-provision upstream).
4. Elevate it (more capacity, sharding, a faster algorithm).
5. Repeat — the constraint moves.

A common failure mode is optimizing the stage that is easiest to measure rather than the one
that is saturated.

### 2.5 The Roofline model (compute vs memory)

`Performance = min(π, β × I)` where `π` is peak compute, `β` is peak memory bandwidth, and `I`
is **arithmetic intensity** (operations per byte moved). Compute-bound code lies below the
sloped roof; memory-bound code lies under the flat roof. Most server code — JSON parsing,
copying buffers, hashing — is memory-bound, which is why reducing allocations and copies beats
micro-optimizing arithmetic. The same reasoning applies one level up: if a service spends its
time waiting on the network/disk, faster CPU code is irrelevant.

### 2.6 Bandwidth-delay product (BDP)

`BDP = bandwidth × RTT`. On a 1 Gbit/s link with 0.5 ms RTT, 62.5 KB can be "in flight" at once.
Below that, throughput is limited by round trips, not bandwidth. This is why a single TCP
connection on a high-RTT link needs large socket buffers or parallelism, and why "just add
bandwidth" doesn't fix a chatty protocol.

### 2.7 The throughput-latency trade-off curve

For a fixed resource, throughput and latency are coupled through queueing: pushing utilization up
raises throughput until the knee, then latency grows with little throughput gain. Every batching
or pooling decision moves you along this curve. The engineering question is always **where on the
curve should this workload sit**, and the answer is derived from the SLO, not from taste.

---

## 3. The measurement method

Performance work without measurement is folklore. The loop:

### 3.1 Define the workload and the target

Write down:
- **Operation mix** (e.g., 95% telemetry writes, 4% claim writes, 1% reads).
- **Arrival pattern** (constant rate? bursts? diurnal? open or closed loop?).
- **Payload sizes** and their distribution (not just the mean).
- **Concurrency** (how many independent clients).
- **SLO** for latency, throughput, and error rate.

A benchmark that doesn't match the production workload measures the benchmark, not the system.
This is the most common cause of "but it was fast in the lab".

### 3.2 Baseline first

Measure the unmodified system at the target load. Record: throughput, p50/p95/p99, error rate,
CPU, RSS, GC stats, disk IOPS, network bytes. No baseline, no proof of improvement.

### 3.3 Instrument, then profile

Two distinct activities:
- **Instrumentation** (always on): counters, histograms, traces, logs. Answers "what is slow?" at
  the level of endpoints and stages.
- **Profiling** (on demand, high overhead): CPU, heap, block, mutex, syscall. Answers "where in the
  code?".

Use instrumentation to find the slow stage; use a profiler on that stage only. Profiling a healthy
service wastes time and can mislead.

### 3.4 Change one variable at a time

A/B only works if A and B differ in one dimension. Changing batch size *and* pool size *and*
serialization together produces a number you cannot attribute.

### 3.5 Statistics: warm-up, repetitions, confidence intervals

- Discard warm-up (JIT-like effects, caches, connection establishment, GC settling).
- Run enough repetitions that noise is smaller than the effect you care about.
- Compare distributions with a tool that reports confidence intervals (`benchstat` for Go
  benchmarks) rather than eyeballing two numbers.
- Watch for **outliers**: they are often the real story (GC, compaction, retries), not noise to be
  trimmed away.

### 3.6 Measurement validity

- **Dead-code elimination:** compilers remove work whose result is unused; benchmark results must
  be consumed (e.g., assigned to a package-level sink).
- **Observer effect:** profiling, tracing, and verbose logging change timing; use sampling and
  measure with them off when quoting numbers.
- **Noisy neighbors:** containers, hyperthreads, CPU frequency scaling, and background daemons
  add variance. Pin CPUs or measure in a quiet window; report the environment.
- **Closed vs open loop** (§8.3) changes which latency you are measuring.
- **Same-machine effects:** loopback networking skips NIC, interrupt, and often some TCP
  processing. Numbers measured on loopback are optimistic for a multi-machine deployment.

### 3.7 Close the loop: regression prevention

Performance is a property that decays. Put benchmarks in CI, store results over time, and fail
(or at least flag) changes beyond a threshold. Profiles should be captured as artifacts on every
release so a regression can be compared against a known-good baseline.

---

## 4. The cost model of the stack

You cannot reason about performance without orders of magnitude. The engineer's job is to know
*which* cost dominates at each layer.

### 4.1 CPU and memory hierarchy (order-of-magnitude latencies)

| Operation | Approx. latency | Relative |
|---|---|---|
| L1 cache hit | 0.5–1 ns | 1× |
| L2 cache hit | 3–10 ns | ~10× |
| L3 cache hit | 10–40 ns | ~40× |
| DRAM access | 60–100 ns | ~100× |
| Uncontended mutex lock | 10–20 ns | |
| Function call / branch mispredict | 1–20 ns | |
| Channel operation (Go) | 50–200 ns | |
| Syscall (cheap, e.g. `getpid`) | 50–500 ns | |
| Socket write (small) | 1–5 µs | |
| Context switch | 1–5 µs | |
| Loopback RTT | 20–100 µs | |
| Same-DC RTT | 0.1–0.5 ms | |
| NVMe random read | 20–100 µs | |
| `fsync` on SSD | 0.1–10 ms | |
| Same-continent RTT | 30–80 ms | |

Consequences: a cache miss costs ~100× an L1 hit; a syscall costs ~1000×; a disk sync costs
~10,000–100,000×. Performance work is largely about **avoiding the expensive rows** — fewer
syscalls, fewer allocations (which cause cache misses and GC), fewer round trips, fewer fsyncs.

Also: **false sharing** (two cores writing different variables on the same cache line) and
**NUMA** effects can dominate in multi-core code. In Go these are usually mild, but hot counters
updated by all goroutines are a classic source of cache-line ping-pong.

### 4.2 Concurrency models and the Go runtime

Three broad models:

| Model | Unit | Switch cost | Strength | Weakness |
|---|---|---|---|---|
| OS threads | kernel thread | µs, kernel-mediated | simple, true parallelism | memory/stack cost, limited count |
| Event loop | callback/future | ns, user-space | cheap I/O concurrency | callback complexity, one slow handler blocks all |
| M:N (Go, Erlang) | goroutine/green thread | ~100 ns–1 µs | simple blocking code + cheap concurrency | runtime complexity, GC |

Go's scheduler multiplexes `G`oroutines onto `M` OS threads via `P` logical processors (the GMP
model). Blocking syscalls hand off the P; network I/O goes through the **netpoller** (epoll/kqueue)
so a blocked socket read parks the goroutine, not the thread. Consequences:

- A goroutine costs a small initial stack (~2 KB, grows) plus scheduler bookkeeping. Tens or
  hundreds of thousands are feasible; millions strain memory and GC.
- `GOMAXPROCS` should match the CPU quota. In containers, older Go versions read host CPUs, not
  cgroup limits, causing oversubscription; modern Go is better but still verify.
- **Scheduler latency** is real: with thousands of runnable goroutines, a goroutine can wait
  hundreds of µs to milliseconds to be scheduled. "Near-zero jitter" is marketing, not a
  guarantee; measure.
- **Timers:** each `time.Ticker` is cheap individually, but N goroutines waking at f Hz produce
  N×f timer events per second. 5,000 goroutines at 100 Hz = 500,000 wakeups/s — measurable
  scheduler and syscall load. Coalescing ticks (one scheduler, batch work) is often better than
  per-entity tickers.
- **Channels** are convenient but not free (~50–200 ns/op plus contention); for extreme rates,
  batching through a queue or per-worker state beats one channel message per event.
- The scheduler is work-conserving and work-stealing, which gives good throughput but not
  hard real-time guarantees. Tail latency from GC and scheduling is the usual reason a Go
  service's p99 is worse than its p50.

### 4.3 Allocation and garbage collection

Go is garbage-collected; the cost of an allocation is not the `new` itself but (a) zeroing,
(b) cache pressure, (c) GC marking/sweeping, and (d) pointer chasing that defeats the cache.
Key facts:

- **Escape analysis** decides stack vs heap. `go build -gcflags='-m'` shows decisions. Values
  that don't escape are stack-allocated and free.
- **Allocation rate** (bytes/s), not heap size, drives GC frequency. `GOGC` (default 100) targets
  heap = 2× live heap; raising it trades memory for fewer cycles, lowering it does the reverse.
  `GOMEMLIMIT` (Go 1.19+) gives a soft memory ceiling and is the right knob in containers.
- **`sync.Pool`** amortizes short-lived buffers but is not a cache: items may be dropped at any
  GC. Use it for hot-path scratch buffers, not for expensive-to-build objects.
- **Pointer-heavy structures** (maps of pointers, linked lists, `[]interface{}`) are cache-hostile.
  Contiguous slices, structs-of-values, and preallocated capacity are faster.
- **GC pauses** in modern Go are sub-millisecond for typical heaps, but *assist* work (goroutines
  helping mark) and heap growth still steal CPU. At very high allocation rates the GC can consume
  a significant fraction of cores. Measure with `GODEBUG=gctrace=1` and `pprof`.

### 4.4 Syscalls and I/O

- A syscall is a privilege transition: ~0.1–5 µs. 100k syscalls/s is ~0.1–0.5 core; 10M/s is a
  bottleneck.
- **Buffering** amortizes syscalls (`bufio`), but adds latency until flush. This is the same
  latency/throughput trade as batching.
- **Vectored I/O** (`writev`, `readv`) writes multiple buffers in one syscall; **`sendmmsg`/
  `recvmmsg`** batch datagrams. Libraries use these to cut per-message cost.
- **Zero-copy** (`sendfile`, `splice`) avoids user-space copies for file→socket. Relevant for
  static file serving, not for JSON APIs.
- **Page cache** makes reads fast until the working set exceeds RAM. Writes are fast until dirty
  page writeback throttles them.
- **`fsync`** is the durability cost: 0.1–10 ms. Databases batch fsyncs (group commit) precisely
  to amortize it. A database's "durable write" latency is usually dominated by this, not by CPU.
- **epoll/kqueue/io_uring:** readiness vs completion interfaces. Go's netpoller uses epoll/kqueue;
  io_uring can reduce syscall overhead further but is not the default in the Go runtime.

### 4.5 The network

- **RTT dominates** request/response protocols. A protocol that needs 3 round trips costs 3×RTT
  before any work happens. Connection reuse removes handshake RTTs; keep-alive is the default in
  most HTTP clients but must be configured and not accidentally disabled.
- **Nagle's algorithm** coalesces small writes, adding up to ~40 ms latency in pathological cases.
  `TCP_NODELAY` (Go's `net.TCPConn.SetNoDelay`, and HTTP servers set it by default) disables it.
  For many small messages, Nagle + delayed ACK is a classic latency trap.
- **Socket buffers** bound in-flight data; small buffers on high-BDP links cap throughput.
- **TLS** adds a handshake (1–2 RTT) and per-record crypto; session resumption amortizes the
  handshake; record overhead is small but nonzero. On loopback, TLS cost is mostly CPU.
- **MTU/fragmentation:** large payloads fragment; UDP-based protocols must handle loss.
- **Head-of-line blocking:** TCP delivers bytes in order, so one lost packet stalls all streams
  on that connection, including HTTP/2 multiplexed streams. WebSocket over TCP inherits this.

### 4.6 Serialization and framing

| Format | Encode/decode cost | Size | Schema | Human-readable | Best for |
|---|---|---|---|---|---|
| JSON | high (reflection, allocations) | large | optional | yes | debuggability, low-to-medium rates |
| Protobuf | low | small | required | no | high-rate RPC, stable schemas |
| MessagePack/CBOR | medium | small | none | no | compact JSON-like payloads |
| FlatBuffers/Cap'n Proto | very low (zero-copy reads) | small | required | no | read-heavy, latency-critical |
| Custom binary | lowest | smallest | manual | no | fixed, hot formats |
| CSV | low | medium | none | yes | bulk import/export |

Two rules of thumb:
1. Serialization cost is proportional to **allocations and branches**, not just bytes. JSON's
   reflection and map/interface handling make it several times slower than Protobuf for small
   objects, but it is rarely the bottleneck below ~10k msg/s.
2. **Compression** trades CPU for bandwidth. For small payloads, per-message gzip is a net loss
   (header + CPU exceed savings). Compress only when payloads are large or bandwidth is scarce,
   and prefer a negotiated, streaming compressor over per-message compression.

**Framing overhead** matters at high message rates: a WebSocket frame adds 2–14 bytes, an HTTP/1.1
request adds ~100–500 bytes of headers (less with HTTP/2 HPACK), and a Protobuf envelope adds a
few bytes. At 4k msg/s, 200 bytes of headers is 0.8 MB/s — negligible on a LAN. At 500k msg/s it is
100 MB/s, which is why *batching* (amortizing one header over many messages) is the standard fix,
not a new protocol.

### 4.7 Interaction styles, and when each is appropriate

| Style | Shape | Connection | Latency | When it wins | Main cost |
|---|---|---|---|---|---|
| Request/response (HTTP/1.1) | client asks, server answers | keep-alive pool | 1+ RTT | independent operations, retries, stateless scaling | per-request overhead |
| HTTP/2 / gRPC | many streams, one connection | multiplexed | 1 RTT | many concurrent RPCs, typed schemas | HOL on loss, flow-control tuning |
| WebSocket | full-duplex messages | one long-lived | 0 RTT after handshake | server push + client messages at high rate | you build acks, backpressure, reconnect |
| SSE | server→client stream | one long-lived HTTP | 0 RTT | one-way push, simple clients | one-way only, proxy buffering |
| Long polling | request held open | keep-alive | ~0 RTT | push without new protocols | connection churn, server resources |
| Message queue | decoupled producer/consumer | broker | variable | buffering, fan-out, durability, backpressure | operational complexity, extra hop |

The decision rule: **match the interaction shape, not the fashion.** If the workload is
request/response with independent operations, HTTP is optimal. If the server must push to many
clients with low latency, SSE or WebSocket. If you need durability and decoupling, a queue. A
persistent connection is not a goal in itself; it is one way to avoid handshake and header costs,
which may or may not be your bottleneck.

---

## 5. Choosing the stack

### 5.1 Language and runtime

Performance is one axis among several; a language decision is usually dominated by ecosystem,
team skill, operability, and time-to-market. Still, the performance-relevant axes are:

| Axis | Why it matters |
|---|---|
| Concurrency model | goroutines vs threads vs async: cost per unit, blocking semantics |
| Memory management | GC pause/throughput vs manual/ownership; RSS predictability |
| Allocation cost | per-object overhead, escape analysis, value vs reference semantics |
| Startup and binary | CLI tools, serverless, containers, cold start |
| Ecosystem | drivers, profilers, observability, libraries |
| Operability | single binary, cross-compilation, runtime flags, debuggability |

Rough positioning (order of magnitude, workload-dependent):

- **Go:** excellent network-service throughput per unit of engineering effort; cheap concurrency;
  GC pauses sub-ms typically; predictable deployment. Weak at allocation-sensitive ultra-low
  latency and at CPU-bound numerical loops.
- **Rust/C++:** lowest and most predictable latency, no GC, fine control of memory layout; higher
  development cost and risk.
- **Java/Kotlin/Scala:** excellent throughput and mature tooling (JFR, async-profiler); JIT
  warm-up and heap tuning; strong for large services.
- **Python/Ruby/Node:** fastest to build; GIL/callback models and interpreter overhead cap CPU-bound
  throughput; often fine when I/O-bound and horizontally scaled.
- **Erlang/Elixir:** superb concurrency and fault tolerance; different performance envelope.

The performance engineer's answer to "which language?" is: **the one whose cost model you can
reason about and whose ecosystem solves your actual bottleneck**, then measure. Language choice
rarely rescues a bad architecture (unbounded queues, chatty protocols, hot partitions).

### 5.2 Runtime and deployment effects

- **Container CPU limits** cause throttling (cgroup quota), visible as p99 spikes at period
  boundaries; `GOMAXPROCS` should respect the quota.
- **Memory limits** interact with the GC (`GOMEMLIMIT`) and the OOM killer.
- **Shared hosts / noisy neighbors** add variance; dedicated CPU pinning helps tail latency.
- **Cold start** matters for CLI tools and autoscaled services: binary size, initialization,
  lazy connection setup.
- **Observability hooks** (profiling endpoints, metrics) should exist in the binary from day one;
  retrofitting them is harder than carrying them.

---

## 6. Architectural performance patterns

### 6.1 Batching and coalescing

Batch `N` items or wait up to `T` before sending. Cost model: with per-item overhead `c` and batch
overhead `C`, total cost ≈ `C/T_batch + c` per item; latency increases by up to `T`. Optimal batch
size balances throughput gain against the latency budget and the maximum acceptable queue delay.

Variants:
- **Time-based flush** (every 10–50 ms) — simple, bounded latency.
- **Size-based flush** (N items or T ms, whichever first) — bounds memory too.
- **Adaptive batching** — size grows when load grows, shrinks when idle; used by databases and
  message brokers.
- **Group commit** — batch `fsync`s: one disk sync for many transactions. This is why databases
  can do thousands of durable writes per second on one disk.
- **Write coalescing** — merge small socket writes with `writev` within a tiny window (gocql, for
  example, coalesces writes for ~200 µs by default).
- **Nagle** is the TCP-level version, and a cautionary tale: it batches without regard to your
  latency budget.

Batching is the highest-leverage optimization in most high-rate systems, because it attacks
per-message fixed costs (syscalls, headers, locks, fsyncs) that dominate at scale.

### 6.2 Pooling and reuse

Four kinds, often confused:
- **Connection pools** (HTTP keep-alive, DB sessions): avoid handshake and TCP slow start. Size
  with Little's Law: `pool ≥ λ × W`. Too small caps throughput; too large consumes memory and
  increases DB-side concurrency (which can *reduce* throughput via contention).
- **Buffer pools** (`sync.Pool`, byte slices): avoid allocation and zeroing in hot paths.
- **Object pools**: reuse expensive objects; in GC languages this often *loses* to just allocating,
  because short-lived objects are cheap and pooling adds synchronization and lifetime bugs.
- **Worker/goroutine pools**: bound concurrency to protect a downstream resource; unbounded
  goroutines are a common way to turn a slow dependency into an OOM.

Pooling must handle **misses** gracefully (create on demand, cap total), **staleness** (health
checks, max lifetime), and **leaks** (always return on every path, including errors). A leaked pool
looks exactly like a throughput cliff and a latency spike.

### 6.3 Concurrency windows and pipelining

Instead of `N` clients each doing `request → wait → request`, keep a window of `W` in-flight
requests per connection/client. Throughput becomes `W / RTT` instead of `1 / RTT`. This is what
HTTP/2, gRPC, and database drivers do. The window must be bounded (Little's Law) and the
downstream must be able to absorb it; unbounded pipelining just moves the queue into the server.

### 6.4 Caching

- **Read-through / write-through / write-behind**: where the cache sits relative to the write path.
- **Snapshot cache**: precompute an aggregate view on a timer; serve many readers from one
  computation. Ideal for dashboards (one ticker, N clients).
- **HTTP caching**: `ETag`/`If-None-Match`, `Cache-Control`, conditional requests turn repeated
  polls into cheap `304`s.
- **Invalidation** is the hard part: TTL is simple and stale; event-driven invalidation is fresh
  and complex. Choose based on the freshness SLO.
- **Stampede protection**: when a hot key expires, many clients recompute simultaneously; use
  single-flight (`golang.org/x/sync/singleflight`) or probabilistic early refresh.
- **Cache sizing**: hit rate vs working-set size; measure hit ratio, not just latency.

### 6.5 Partitioning and hot keys

Throughput scales with the number of independent units of work: shards, partitions, cores, nodes.
The limit is always the **hottest** unit, not the average. Design keys so that load is uniform and
so that the dominant query is served by one partition. Watch for:
- sequential/monotonic keys (all writes to the newest shard),
- celebrity/hot keys,
- partition growth without a time or size bound,
- cross-partition operations (fan-out, scatter-gather) on the hot path.

### 6.6 Backpressure, admission control, and load shedding

A system without backpressure converts overload into latency, memory growth, and finally collapse.
The toolkit:
- **Bounded queues** everywhere. Unbounded queues are delayed OOMs.
- **Admission control**: reject early when the queue is full (`429`/`503` + `Retry-After`) rather
  than accepting work you cannot finish.
- **Load shedding**: drop low-priority work (telemetry samples) before high-priority work (floor
  claims).
- **Circuit breakers**: stop calling a failing dependency; fail fast and recover.
- **Retries with exponential backoff and jitter**: retries without jitter synchronize clients into
  a thundering herd. Cap attempts and total time.
- **Timeout budgets**: a request's deadline must shrink as it traverses the call graph; a child
  must never outlive its parent's deadline.
- **Bulkheads**: separate pools/queues per traffic class so a slow class cannot starve a fast one.
- **Idempotency**: retries are only safe if the operation is idempotent (natural key, request ID,
  upsert). Design the data model for it.

### 6.7 Asynchronous processing and queues

Decoupling producer and consumer absorbs bursts and isolates failure, at the cost of an extra hop,
operational complexity, and eventual consistency. At-least-once delivery requires idempotent
consumers. Use queues when the producer's latency SLO should not depend on the consumer's
throughput; avoid them when the workload is a simple request/response and the queue would only add
latency and moving parts.

### 6.8 Data layout and locality

- **Struct-of-arrays vs array-of-structs**: SoA is faster when scanning one field over many items
  (cache lines carry only useful bytes).
- **Contiguity**: slices beat linked structures; preallocate with `make([]T, 0, n)`.
- **Columnar storage/compression** in databases: fewer bytes read, better cache use.
- **Locality**: process data where it lives; avoid copying between layers (zero-copy where safe).

---

## 7. Storage and database performance

### 7.1 The LSM write path (Cassandra, Scylla, RocksDB)

A write is typically: append to a **commit log / WAL** (sequential, batched fsync), update an
in-memory **memtable**, acknowledge, and later flush the memtable to an immutable **SSTable** on
disk. Background **compaction** merges SSTables, reclaiming overwritten/expired data.

The performance consequences:
- Writes are fast because they are sequential appends; **durability is batched** (group commit).
- **Write amplification** (data rewritten by compaction), **read amplification** (multiple SSTables
  to check), and **space amplification** (garbage awaiting compaction) are the fundamental taxes.
- **Tombstones** (deletes, TTL expiry, nulls) accumulate and slow reads; a delete-heavy workload
  can be worse than the writes it replaces.
- Compaction competes for disk and CPU; an overloaded compaction backlog shows up as read latency
  spikes, not write errors.

### 7.2 Replication and consistency cost

With replication factor `R` and consistency level `CL`:
- A write waits for `CL` replicas to acknowledge; a read contacts enough replicas to satisfy `CL`
  and reconciles. `QUORUM = ⌊R/2⌋ + 1` gives read-your-writes when `W + R > R`.
- Latency ≈ the slowest of the required replicas plus coordinator overhead. `ONE` is fastest and
  weakest; `ALL` is slowest and fails on any replica outage.
- **Quorum is a latency multiplier**, not just a correctness setting: each operation becomes
  `max()` over several nodes, so tail latency is the max of tails.
- **Token-aware routing** sends the request to a replica that owns the data, removing a coordinator
  hop. Without it, every request pays an extra network hop.
- **Hinted handoff and repair** consume resources after failures; retries amplify load during
  incidents.
- **Idempotency** determines whether the driver may retry safely. Non-idempotent operations
  (counters, appends) must not be retried blindly.

### 7.3 Batches, counters, LWT, indexes

- **Logged batches** add a batch log write for atomicity; **unlogged batches** are just a
  multi-statement message (still not a transaction across partitions). Large cross-partition
  batches are an anti-pattern (coordinator hotspot, latency).
- **Counters** require read-modify-write per replica and are non-idempotent; high-frequency counter
  updates are a known scalability trap. Aggregate before writing.
- **Lightweight transactions (LWT/Paxos)** cost multiple round trips (often 4× a normal write) and
  serialize on the partition. Avoiding them is a legitimate design goal, but the replacement
  protocol must be analyzed for its own cost.
- **Secondary indexes** are local and scatter-gather across nodes; they are not a substitute for a
  query-driven table design.

### 7.4 Partition design

Two independent limits:
- **Cell count** per partition (2×10⁹ in Cassandra) — a hard correctness limit.
- **Partition size** (~100 MB practical guidance, lower is better) — a performance/operability
  limit driven by compaction, repair, and read cost.

A time-bucketed key bounds *time*, not *size*: bucket size in rows = `bucket_seconds × rate`.
If rate grows, the partition grows. Remedies: bound by size (shorter buckets, hash shards), and
keep the read pattern in mind (the common query should touch one bucket, not all of them).

**Hot partitions**: even a small partition is a hotspot if all traffic targets it. A per-room key
is fine while claims are rare; it becomes a serialization point when hundreds of racers hit it.
Sharding by `hash(user_id) % K` spreads writes but complicates reads (fan-out).

### 7.5 TTL, tombstones, and retention

TTL is the zero-maintenance retention mechanism: rows expire during compaction without client
deletes. But TTL'd data still creates compaction work, and **expired-but-not-yet-compacted** rows
remain readable until purge. A TTL shorter than the compaction cycle means a large fraction of
reads can hit expired data. Also, TTL is per-cell; rewriting a row with a different TTL changes
its expiry. Plan retention against compaction throughput.

---

## 8. The toolbox

### 8.1 Go-specific

```bash
# Microbenchmarks: always with -benchmem and repetitions
go test -run '^$' -bench . -benchmem -count=10 ./internal/... | tee new.txt
benchstat old.txt new.txt                     # confidence intervals, not vibes

# Profiles (CPU, heap, allocs, block, mutex, goroutine)
go test -run '^$' -bench . -cpuprofile cpu.out -memprofile mem.out
go tool pprof -http=:8080 cpu.out
go tool pprof -alloc_space mem.out

# Runtime tracing (scheduler, GC, syscalls, blocking)
go test -run '^$' -bench . -trace trace.out
go tool trace trace.out

# Allocation/escape decisions
go build -gcflags='-m=2' ./... 2>&1 | grep -E 'escapes|moved to heap'

# GC telemetry
GODEBUG=gctrace=1 ./app backend

# Race detector (correctness, but it changes timing)
go test -race ./...

# Profile-guided optimization: feed a real CPU profile back into the build
go build -pgo=cpu.pprof ./cmd/...
```

In-process HTTP profiling (development only):

```go
import _ "net/http/pprof"   // exposes /debug/pprof on the default mux
```

Also: `expvar` for counters, `gops` for live process inspection, `testing.AllocsPerRun` for
allocation assertions, `benchstat` for statistics, `goleak` for goroutine leaks.

### 8.2 OS, CPU, and network

```bash
perf stat -d ./app backend            # cycles, instructions, cache misses, context switches
perf record -F 99 -g -- ./app backend # sampling profile (use --call-graph dwarf if needed)
perf top
bpftrace -e 'tracepoint:syscalls:sys_enter_write { @[comm] = count(); }'   # syscall counts
strace -c -f ./app backend            # syscall time breakdown
ss -s; ss -tinp                        # socket states, queues, cwnd, rtt
nstat -az                             # network stack counters (retransmits, drops)
tcpdump -i lo -w cap.pcap; tshark -r cap.pcap -q -z io,stat,1
iostat -x 1                            # disk utilization, queue depth, await
vmstat 1                               # run queue, context switches, swap
pidstat -w -u -r 1                     # per-process CPU/mem/context switches
sar -n DEV 1                           # interface throughput
tc qdisc add dev lo root netem delay 5ms loss 0.1%   # emulate a real network
taskset -c 0-3 ./app backend           # pin CPUs to reduce variance
docker stats                           # container CPU/mem/IO
cat /sys/fs/cgroup/cpu.stat            # throttling (nr_throttled)
```

### 8.3 Load generators: closed vs open loop

- **Closed loop** (`ab`, `hey` default, `wrk` default): each virtual user waits for a response.
  Simple, but suffers **coordinated omission**; it under-reports tail latency under overload.
- **Open loop** (`wrk2 -R`, `vegeta -rate`, `k6` with constant arrival rate, `fortio`): requests
  arrive at a fixed rate regardless of completion. This is what production traffic looks like and
  what you need for tail SLOs.

```bash
hey -c 64 -z 30s -m POST -T application/json -d @payload.json http://host/endpoint
wrk2 -t4 -c64 -R4000 -d60s --latency http://host/endpoint
vegeta attack -rate=4000 -duration=60s -targets targets.txt | vegeta report -type=hdrplot
k6 run --vus 64 --duration 60s script.js
ghz --insecure --proto api.proto --call pkg.Service/Method -d @req.json -c 64 -n 100000 host:port
```

Compare **service time** (server-side) with **response time** (client-side): the gap is queueing
and network. Use both.

### 8.4 Database and storage

```bash
cassandra-stress write n=1000000 -rate threads=64 -node 127.0.0.1
cassandra-stress mixed ratio\(write=9,read=1\) -rate threads=64
nodetool tablestats anti_interrupt.audio_telemetry   # partition sizes, SSTables, read/write latency
nodetool tablehistograms anti_interrupt.audio_telemetry
nodetool proxyhistograms                              # coordinator latency percentiles
nodetool tpstats                                      # thread pool saturation, dropped messages
nodetool compactionstats                              # compaction backlog
nodetool gcstats                                      # GC pauses
nodetool netstats                                     # streaming/repair traffic
cqlsh -e 'TRACING ON; SELECT ...;'                    # per-query trace with replica timings
```

`nodetool tpstats` is the Cassandra equivalent of "which stage is saturated": dropped
`MutationStage`/`ReadStage` messages, pending compactions, and `Native-Transport-Requests` queue
depth tell you where the cluster is hurting. Pair with JMX metrics (Prometheus `jmx_exporter`) and
Grafana for time series.

### 8.5 Observability and continuous profiling

- **Metrics:** Prometheus + Grafana; RED metrics (Rate, Errors, Duration) per endpoint; histograms
  not averages.
- **Tracing:** OpenTelemetry; span per stage (HTTP → queue → DB) to see where the time goes.
- **Continuous profiling:** Pyroscope / Parca / Grafana Alloy; always-on sampled CPU/heap profiles
  tied to deploys, so regressions are diffable.
- **Logging:** structured (`log/slog`) with latency fields; avoid hot-path logging in benchmarks.
- **Synthetic checks:** run the benchmark suite on a schedule, not just on commits.

### 8.6 Experiment hygiene checklist

- [ ] Workload documented (mix, sizes, arrival process, concurrency).
- [ ] Environment documented (CPU, RAM, kernel, Go version, container limits, network path).
- [ ] Warm-up discarded; repetitions ≥ 5–10; confidence intervals reported.
- [ ] One variable changed.
- [ ] Client and server metrics captured; queue depth and error rate included.
- [ ] Tail percentiles and max reported; open-loop generator used for SLO claims.
- [ ] Profiler overhead accounted for (numbers quoted from non-profiled runs).
- [ ] Results stored as artifacts with the commit hash.

---

## 9. Capacity planning

1. **Measure the unit cost** of each stage: CPU-µs/request, bytes/request, DB writes/request,
   p50/p99 latency.
2. **Compute per-node capacity** at the target utilization (60–70%), not at saturation:
   `capacity = 1 / (service_time × (1 + queue_factor))`.
3. **Add headroom** for failure: N+1 (or N+2 for quorum systems), plus burst capacity.
4. **Check the end-to-end budget**: the slowest stage's p99 plus queueing must fit the SLO.
5. **Cost it**: `$/M requests = (CPU-hours × $/CPU-hour + storage + network) / M requests`.
6. **Re-measure after every architectural change**; capacity plans age badly.

Example: a stage with 2 ms p50 and 10 ms p99 at 60% utilization has a throughput ceiling of
`1/0.002 = 500/s per worker`; with `L` workers, `throughput ≈ L × 500` only if `L` matches the
downstream's ability to absorb it. Little's Law ties the three numbers together.

---

## 10. Heuristics and anti-patterns

**Do:**
- Measure before optimizing; optimize the bottleneck.
- Prefer fewer, larger operations (batch/coalesce) over many small ones.
- Bound every queue, pool, and retry loop.
- Make retries idempotent; add jitter.
- Set deadlines and shrink them across calls.
- Keep utilization ≤ ~70%; design for the tail.
- Serve dashboards from precomputed snapshots.
- Reuse connections and buffers in hot paths.
- Write down the latency budget per stage.

**Don't:**
- Add a protocol/technology before measuring the current one.
- Use unbounded goroutines/queues as a concurrency strategy.
- Retry non-idempotent operations.
- Optimize averages; ignore p99/max.
- Put per-event logging in the hot path.
- Assume a persistent connection is inherently faster; handshakes are amortized by keep-alive.
- Shard by time without bounding partition size; shard by room without bounding per-room rate.
- Trust a single benchmark run; trust loopback numbers for a multi-machine deployment.
- Confuse "it compiles and is fast in dev" with "it meets the SLO under load".

---

## 11. Glossary

| Term | Meaning |
|---|---|
| **SLO / SLI** | Service Level Objective (target) / Indicator (measurement) |
| **p99** | 99th percentile latency: 1% of requests are slower |
| **Coordinated omission** | Load-generator artifact that hides latency during stalls |
| **Open/closed loop** | Fixed arrival rate vs wait-for-response load generation |
| **Little's Law** | `L = λW`: concurrency = rate × time in system |
| **Utilization (ρ)** | Arrival rate / service rate; queueing explodes near 1 |
| **Amdahl / USL** | Speedup limits from serialization / coherency |
| **Roofline** | `min(peak compute, bandwidth × arithmetic intensity)` |
| **BDP** | Bandwidth × RTT: bytes needed in flight to fill a link |
| **HOL blocking** | One stalled item blocks all items behind it in an ordered stream |
| **Backpressure** | Signaling upstream to slow down when downstream is saturated |
| **Group commit** | Batching durable writes/fsyncs into one disk operation |
| **Write amplification** | Bytes written to storage per logical byte (LSM compaction) |
| **Tombstone** | Marker for deleted/expired data; read and compaction cost |
| **Hot partition/key** | A shard receiving disproportionate traffic; limits scaling |
| **Quorum** | `⌊R/2⌋+1` replicas; latency is the max over required replicas |
| **Idempotent** | Repeating the operation has the same effect as doing it once |
| **Escape analysis** | Compiler decision: stack vs heap allocation |
| **GC assist** | Goroutines doing GC marking work; steals CPU at high allocation rates |
| **Netpoller** | Go runtime's epoll/kqueue integration for non-blocking I/O |
| **False sharing** | Cores invalidating each other's cache lines on adjacent variables |

---

## 12. Further reading

- Kleppmann, *Designing Data-Intensive Applications* — storage engines, replication, consistency.
- Gunther, *Guerrilla Capacity Planning* — USL, queueing, scalability.
- Gregg, *Systems Performance* — the OS-level measurement methodology; also *BPF Performance Tools*.
- Harchol-Balter, *Performance Modeling and Design of Computer Systems* — queueing theory.
- Little, "A Proof for the Queuing Formula: L = λW" (1961).
- Go documentation: `runtime`, `runtime/pprof`, `runtime/trace`, `net/http/pprof`, `sync.Pool`,
  `context`, and the Go blog posts on the scheduler and GC.
- Apache Cassandra documentation: *Architecture*, *Data Modeling*, and the `nodetool` reference.
- Google SRE Book, chapters on SLOs, load balancing, and handling overload.
- Dean & Barroso, "The Tail at Scale" (2013).
