# Implementation Tasks — Anti-Interrupt

> **Companion to `PROJECT.md`.** That document is the canonical specification; this one turns it into
> a work backlog. Read `PROJECT.md` §4–§10 before starting any story in Epics 1–6.
>
> **No code is provided here.** The intent is that each story gives you enough context, structure,
> and acceptance criteria to implement it by hand.

---

## How to use this backlog

### Story format

Every story has:
- **Priority** and **depends on**
- **Story** — the user-visible or developer-visible outcome
- **Why** — the problem and the project requirement it serves
- **Implementation notes** — package boundaries, data flow, pitfalls, configuration
- **Testing** — unit, integration, scenario, and manual checks
- **Done when** — objective acceptance criteria

### Priorities

| Priority | Meaning |
|---|---|
| **P0** | Blocking: all later work is unreliable or unmeasurable without it |
| **P1** | Core lab capability: required for the six assumptions and the benchmark |
| **P2** | Important for usability, analysis quality, or multi-machine operation |
| **P3** | Polish and defense quality |

### Global Definition of Done

A story is not done until:

- The package builds and `go vet` passes; `go test -race` passes for touched packages.
- Pure logic has unit tests; Cassandra-touching code has an integration test behind a build tag.
- Every blocking operation takes a `context.Context`; cancellation is tested.
- No LWT, no `DELETE` on floor state, no `ALLOW FILTERING` in a domain path.
- No unbounded queue, goroutine set, or retry loop is introduced.
- Logs include `run_id`/`client_id` where applicable; new latency/counter metrics are registered.
- `PROJECT.md` is updated if a key, protocol step, lifecycle rule, or demonstration mechanism
  changed.

### Suggested milestones

| Milestone | Stories | Outcome |
|---|---|---|
| **M0 — Foundations** | US-001…006 | Config, migrations, session, observability, command skeleton, test harness |
| **M1 — Telemetry** | US-010…012, 017 | One room writes frames at 100 Hz with metrics |
| **M2 — Floor control** | US-013…016 | Claims, heartbeats, ledger, fairness |
| **M3 — Agent** | US-020…022 | Remote start/stop of runs, lifecycle, caps |
| **M4 — Visibility** | US-030…034, 040…042 | Stats schema, snapshots, observer, TUI |
| **M5 — Audit/report** | US-050…054 | Token scan, dedupe, lookup, report |
| **M6 — Lab matrix** | US-060…069 | Each assumption demonstrated with evidence |
| **M7 — Scale/defense** | US-023, 070…075, 080…083 | Multi-machine runs, analysis artifacts, docs |

### Testing layers

1. **Unit** — pure logic: bucketing, batching, priority, fairness, run registry, dedupe, lifecycle.
   Use small interfaces/fakes for the store; do not mock the driver deeply.
2. **Integration** — real Cassandra from `docker compose`; unique `run_id`/`room_id` per test;
   cleanup by TTL or explicit table truncation; build tag `integration`; `-race`.
3. **Scenario** — a named mode (`races`, `skew`, `collision`, …) with a seed; asserts a correctness
   oracle, not just "no error".
4. **Chaos** — application-level injection first (clock offset, forced timestamp, timeout, delay);
   infrastructure chaos (`docker stop`, `tc netem`) second, always recorded in the manifest.
5. **Performance** — `testing.B` for micro paths; open-loop generator for tails; `benchstat` for
   comparisons; never quote loopback numbers as multi-machine results.
6. **Golden** — report output, TUI frames, manifest schema, JSON payload schema.

### Hand-implementation advice

- Work in **vertical slices**: one room end-to-end before ten.
- Write the **oracle first** for correctness stories (exactly one winner, zero torn rows, exact
  ledger), then make it pass.
- Keep `domain`, `arbiter`, `accounting`, and `auditor` free of HTTP and cobra imports; they should
  be testable without a server.
- Treat the agent as a thin composition layer. If logic starts accumulating there, move it down.
- Prefer a small number of explicit interfaces over broad abstractions; the store interface for the
  claim state machine should expose only the operations the state machine needs.

---

# Epic 0 — Foundations

## US-001 — Load, merge, and validate configuration

**Priority:** P0 · **Depends on:** none

**Story.** As a developer, I want one effective configuration per process, validated once, so that
`run`, `agent`, and `tui` behave identically and every benchmark can be reproduced from its recorded
parameters.

**Why.** Backend flags are currently registered inside `RunE`, so they are never parsed and every
run silently uses zero values. Without a reliable config layer, no benchmark result is trustworthy.

**Implementation notes.**
- New `internal/config` package. A single `Config` value populated at command start and passed down.
- Precedence: explicit flags → environment → scenario file → defaults. Document it.
- Validate in one pass and report all errors together (positive rooms/users/freq, known mode,
  duration > 0 for agent runs, keyspace is a safe identifier, nodes non-empty).
- Provide a `--dry-run` that prints the effective configuration (with secrets redacted) and exits.
- Keep `Config` immutable after validation; no package mutates it.
- Remove all flag registration from `RunE` bodies; register flags when the command is constructed.

**Testing.**
- Table-driven validation tests for each invalid field and combinations.
- Precedence tests: flag overrides env, env overrides file, file overrides default.
- Round-trip test: a scenario file produces the same effective config as equivalent flags.
- Manual: `--dry-run` on each subcommand.

**Done when.** No flag is registered inside a `RunE`; invalid config exits non-zero with all errors;
the effective config is logged at startup; a scenario file and equivalent flags produce identical
behavior.

---

## US-002 — Single, fatal, versioned migrations

**Priority:** P0 · **Depends on:** US-001

**Story.** As an operator, I want migrations to run exactly once, fail loudly, and be verifiable, so
that every agent talks to the same schema.

**Why.** `schema.Migrate` currently logs errors and returns `nil`, so a broken migration produces a
"successful" run against missing tables. Concurrent agents running migrations would also race.

**Implementation notes.**
- Keep migrations embedded under `cql/` and applied by a dedicated `migrate` subcommand; agents and
  runs only **verify** the applied version.
- Propagate every migration error; never continue after a failed statement.
- Record applied versions in the migration tool's bookkeeping table; add a schema-version check used
  by `agent`/`run` at startup.
- Document the `CREATE KEYSPACE IF NOT EXISTS` caveat: a changed RF on an existing keyspace is
  silently ignored; changing RF requires `ALTER KEYSPACE` and must be an explicit operation.
- Never edit an applied migration; add a new one.
- Validate keyspace/RF as identifiers/numbers before building DDL; no string interpolation of
  unvalidated input.

**Testing.**
- Apply on an empty database; apply again (idempotent); start a component with a mismatched version
  (must refuse to start).
- Failure injection: a deliberately invalid statement must make the migrator exit non-zero.
- Concurrency: two migrators started together; exactly one applies and the other reports the
  already-applied state without corruption.
- Verify all tables from `PROJECT.md` §5 exist with the expected keys.

**Done when.** `migrate` is the only writer of schema; errors are fatal; version mismatch is
detected; the schema matches `PROJECT.md`.

---

## US-003 — Cassandra session factory and policies

**Priority:** P0 · **Depends on:** US-001

**Story.** As a developer, I want one correctly configured gocql session per process, with
token-aware routing, idempotency-aware retries, and sane timeouts, so that every component shares
the same connection behavior.

**Why.** The current code creates ad-hoc sessions with default policies: no token awareness, default
timeouts of 11 s, and no per-query idempotency. That adds a network hop per write, holds goroutines
during incidents, and makes retries unsafe.

**Implementation notes.**
- New `internal/cassandra` package: build a session from validated config, health-check it at
  startup, close it on shutdown.
- Enable token-aware host policy; confirm the coordinator is a replica on a query trace.
- Configure `NumConns` and `MaxRequestsPerConn` deliberately; document the current defaults.
- Implement a retry policy that respects per-query idempotency: aggressive capped retries for
  idempotent operations, no blind retries for counters. Jitter all backoff.
- Set a per-query context deadline in the low-hundreds-of-milliseconds to low-seconds range,
  depending on the operation; never rely on the 11 s driver default for request paths.
- Prepare statements once and reuse them; never build CQL with raw user input.
- Expose a small store interface used by the domain packages; keep gocql types out of `domain`.

**Testing.**
- Integration: connect, health check, graceful close; query trace shows token-aware routing.
- Unit: retry policy decision table (idempotent vs not, timeout vs unavailable vs overloaded).
- Timeout propagation: a canceled context aborts the query promptly.
- Failure: unreachable cluster fails startup with a clear error.

**Done when.** One session per process; token-aware; idempotency-aware retry policy; no driver
default timeout on request paths; session lifecycle tied to the root context.

---

## US-004 — Observability baseline

**Priority:** P0 · **Depends on:** US-001

**Story.** As an operator, I want structured logs, metrics, and profiles keyed by run, so that I can
attribute every slowdown and error to a stage and a run.

**Why.** The project's value is the analysis; without consistent instrumentation the benchmark data
is anecdotal.

**Implementation notes.**
- New `internal/metrics`: counters, gauges, and histograms. Choose one exposition mechanism
  (Prometheus text or `expvar`) and use it everywhere; keep the HTTP handler small.
- Log with `log/slog` (JSON in lab mode). Add `run_id`, `client_id`, `room_id`, `turn_seq` to the
  logger context where relevant; never log per frame in the hot path.
- Expose `/metrics` on the agent; gate `/debug/pprof` behind a development flag.
- Register per-stage histograms from the start: batch wait, DB write, claim verification read,
  claim acquisition, snapshot write.
- Ensure graceful shutdown flushes counters and closes the profiling endpoints.

**Testing.**
- Endpoint smoke tests for `/metrics` and (dev-only) pprof.
- Assert log records contain the expected fields.
- Race test on metric updates under concurrent writers.
- Verify no per-frame log call exists in the telemetry hot path.

**Done when.** Every stage has a latency histogram and an error counter; logs carry run identity;
metrics are scrapeable; profiling is opt-in.

---

## US-005 — Command skeleton and shared lifecycle

**Priority:** P0 · **Depends on:** US-001, US-004

**Story.** As a user, I want one binary with `migrate`, `run`, `agent`, `tui`, `audit`, and `report`
subcommands that all share one root context and shut down cleanly.

**Why.** The project must be a single deployable binary; the current `backend`/`simulator`/`tui`
split is a stub and the role names no longer match the architecture.

**Implementation notes.**
- Keep `cmd/anti-interrupt/main.go` small: create the signal-aware root context, register
  subcommands, call `ExecuteContext`, map errors to exit codes.
- Register `agent` as the canonical name and keep `backend` as a deprecated alias for one release.
- Add `orchestrator` as an optional subcommand (or a mode of `agent`); do not duplicate logic.
- Each command builds its dependencies from `internal/` and returns an error; no `os.Exit` outside
  `main`.
- Ensure `SIGINT`/`SIGTERM` cancel every command promptly.

**Testing.**
- `--help` works for every subcommand and mentions the key flags.
- Sending SIGINT to a blocking command exits within a bounded time with a clean shutdown log.
- Alias `backend` behaves identically to `agent` (at least in help and startup).
- No command panics on missing config.

**Done when.** The skeleton builds, every command is wired, and shutdown is context-driven.

---

## US-006 — Integration test harness

**Priority:** P0 · **Depends on:** US-002, US-003

**Story.** As a developer, I want a repeatable way to run tests against a real 3-node cluster, so
that driver behavior and schema assumptions are tested rather than mocked.

**Why.** gocql behavior (token awareness, retries, timeouts, batches, collections, counters) cannot
be validated against fakes, and the lab's correctness claims depend on real Cassandra semantics.

**Implementation notes.**
- A test harness that waits for the compose cluster health, then runs integration tests behind a
  build tag.
- Unique `run_id`/`room_id` per test; cleanup by TTL for telemetry and by explicit truncation for
  small tables; never depend on test ordering.
- Skip integration tests with a clear message when the cluster is unavailable.
- Provide helpers for a "known room" fixture, a claim writer, and a telemetry reader.
- Run the suite with the race detector.

**Testing.**
- The harness itself is verified by a trivial read/write/read-back test.
- Two parallel test packages must not interfere (unique keys).
- A failed cleanup must not fail unrelated tests.

**Done when.** `go test -tags integration ./...` is reliable on a clean compose cluster and in CI.

---

# Epic 1 — Participant runtime

## US-010 — Scenario model and deterministic scheduling

**Priority:** P1 · **Depends on:** US-001, US-003

**Story.** As a benchmark author, I want a scenario to fully determine what each participant does, so
that two runs with the same seed produce the same behavior.

**Why.** Reproducibility is a success criterion. Random behavior without a recorded seed makes
comparisons meaningless.

**Implementation notes.**
- New `internal/domain/scenario`: rooms, users per room, frequency, mode, seed, duration, start
  time, clock offset, batching, consistency.
- One scheduler tick per process (or a small number) that advances all participant state machines,
  rather than one OS timer per participant; this controls burstiness and timer overhead.
- Derive a per-participant RNG from the run seed and participant identity; never share a global RNG.
- Support open-loop (fixed rate) and closed-loop (wait-for-ack) generation; record which is used.
- Count generated and dropped ticks explicitly; a ticker that drops ticks must be visible.
- Stop cleanly on context cancellation or duration expiry.

**Testing.**
- Golden schedule tests: same seed → identical event sequence; different seeds → different.
- Duration and `start_at` honored (within tolerance).
- Dropped-tick counter increments when the consumer is artificially stalled.
- Race test on scheduler state.

**Done when.** A scenario is serializable, replayable, and its schedule is deterministic.

---

## US-011 — Telemetry generator and batched writer

**Priority:** P1 · **Depends on:** US-010, US-003

**Story.** As a participant, I want to emit categorical frames at the configured rate and persist
them with TTL, so that A1 (high-frequency telemetry) is exercised end to end.

**Why.** This is the write load the entire project measures. The batching, bucketing, and TTL choices
must be correct before adding floor control.

**Implementation notes.**
- New `internal/telemetry`: generate `A`/`S`/`T` frames; keep the optional realistic profile behind a
  config flag/migration, not in the baseline path.
- Derive `minute_bucket` from the UTC minute of `frame_ts`; never from local wall clock or a cached
  bucket that can drift.
- Batch by size-or-time (e.g., 10–50 frames or 10–50 ms); flush on timer, on size, and on shutdown.
- Write with `USING TTL 600`, idempotent by the natural key; retry with jitter; count failures.
- Bound the outbound queue; when full, drop and count rather than grow memory.
- Make consistency level configurable and record it.

**Testing.**
- Unit: bucket boundary handling (59.999 s → next minute; UTC, not local).
- Unit: batch flushes on both size and timer; no partial batch lost on shutdown.
- Integration: write a batch, read it back, verify TTL expiry after the window.
- Load: 4k frames/s for a fixed duration; no unexplained loss; queue depth bounded.
- Race test on the writer and flush timer.

**Done when.** Telemetry lands in the correct buckets, expires via TTL, survives retries without
duplicates, and reports generated/acked/dropped counts.

---

## US-012 — Noise floor calibration and speaker profiles

**Priority:** P1 · **Depends on:** US-011

**Story.** As a participant, I want a dynamic noise-floor estimate and a bounded calibration history,
so that `snr_margin` and the A6 collection demonstration are meaningful.

**Why.** The floor-control state includes `noise_floor_db` and the coupled invariant; the profile
table demonstrates `list`/`map` operations and is part of the analysis.

**Implementation notes.**
- New `internal/telemetry` (or `internal/domain`) calibration: an EWMA or Ornstein–Uhlenbeck process
  updated per calibration interval, not per 10 ms frame.
- Keep the last 10 samples in memory; persist the bounded list at a low, configurable cadence.
- Device metadata is a small string map; do not update it per frame.
- Be explicit about read-modify-write contention: concurrent calibrations for the same user
  serialize on the profile cell. Choose an owner (one goroutine per user) or accept last-write-wins
  and document it.
- Expose the current estimate to the floor state and to the snapshot payload.

**Testing.**
- Convergence: a step change in noise produces the expected approach; bounds are enforced.
- The list never exceeds 10; map updates survive round-trip.
- Concurrent calibration updates do not race or exceed bounds.
- Integration: profile read/write with TTL/cleanup policy as configured.

**Done when.** The noise estimate is stable, bounded, persisted at the configured cadence, and
visible in the run data.

---

## US-013 — Claim acquisition state machine

**Priority:** P1 · **Depends on:** US-003, US-010

**Story.** As a participant, I want to contest a turn, wait the stabilization window, verify the
ordered claims, and learn whether I won, so that A2 works without LWT.

**Why.** This is the core correctness mechanism of the project. It must be isolated from transport
and storage details so it can be tested exhaustively.

**Implementation notes.**
- New `internal/arbiter` with a store interface: read latest turn, insert claim, read top claims,
  write state, heartbeat, release.
- Flow: determine next open turn → insert at `CL=QUORUM` → wait Δt (configurable, recorded) → read
  top K at `CL=QUORUM` → index 0 wins → transition.
- Use the exact ordering `(priority_tier ASC, claim_ts ASC, user_id ASC)`; do not re-sort in Go.
- No LWT, no read-modify-write of a single active-speaker cell.
- Handle cancellation during the wait; do not leave a claim with an ambiguous local state.
- Record queue position, win/loss, and acquisition latency.
- On a stale turn (no heartbeat), allow the next turn to proceed.

**Testing.**
- Unit: state machine with a fake store for win, loss, stale turn, canceled wait, and duplicate
  claim.
- Property test: N contenders with the same turn always produce exactly one winner.
- Integration: synchronized onset scenario (2–15 ms spread) repeatedly; exactly one winner each time.
- Latency histogram: acquisition ≈ Δt + two quorum round trips.

**Done when.** No LWT is issued; exactly one winner per contested turn; losers queue; every path is
cancellable and observable.

---

## US-014 — Heartbeats, release, and stale-speaker detection

**Priority:** P1 · **Depends on:** US-013

**Story.** As a speaker, I want to prove liveness with a TTL heartbeat and release without deleting,
so that A3's clock-skew workaround is demonstrated.

**Why.** The no-DELETE rule and logical turn numbers are the answer to future-timestamp tombstones;
heartbeats replace liveness that a delete would otherwise signal.

**Implementation notes.**
- Write `floor_heartbeats` every 500 ms with TTL 2 s, keyed by `(room_id, turn_seq, user_id)`.
- Release writes a `RELEASED` claim row for the turn; stop heartbeats; never delete.
- Treat a turn as open when the turn is released/absent and no heartbeat exists.
- Record stale-speaker events (heartbeat expired while the process still thought it was speaking).
- Ensure the heartbeat writer is tied to the speaker's context and stops on cancellation.
- Document the interaction with `--clock-offset`: heartbeats use server-side TTL, so they expire
  regardless of the client's wall clock.

**Testing.**
- TTL expiry: after stopping heartbeats, the row is gone within the TTL window (tolerance).
- Release allows the next turn to be claimed.
- Cancellation stops heartbeats promptly; no goroutine leak.
- Skew scenario: ±60 s offset does not prevent release or the next claim.
- Static check: no `DELETE` statement in floor-control code paths.

**Done when.** Liveness is TTL-based, release never deletes, staleness is detected, and the skew
scenario passes.

---

## US-015 — Completed-turn ledger and aggregated counters

**Priority:** P1 · **Depends on:** US-013, US-014

**Story.** As an analyst, I want an exact turn ledger and a best-effort counter track, so that A5's
idempotency distinction is measurable.

**Why.** Counters are non-idempotent; the lab must show the overcount risk and the ledger-based
correctness path.

**Implementation notes.**
- New `internal/accounting`.
- On turn completion, write one `completed_speech_turns` row with a UUID, duration, and optional
  average RMS; mark idempotent and retry aggressively.
- Update counters once per turn or per second (configurable aggregation window), at `CL=ONE`, marked
  non-idempotent; never retry blindly.
- Record counter write errors separately; a failed increment is data, not a crash.
- Compute priority from the ledger, not from counters.
- Document the exact aggregation window in the run manifest.

**Testing.**
- Ledger: duplicate writes of the same UUID do not double-count; retries are safe.
- Counters: injected timeouts with retry produce a measurable drift; without retry, the drift is
  bounded by lost increments.
- Integration: a turn writes both tracks; totals reconcile in the no-fault case.
- Priority computation matches the ledger-derived expectation.

**Done when.** The ledger is exact under retries; counters are aggregated and non-retried; the
difference is observable in the run data.

---

## US-016 — Fairness and priority engine

**Priority:** P1 · **Depends on:** US-015

**Story.** As a participant, I want my priority tier to reflect my speaking history, so that the
system prevents monopolization and the fairness scenario is demonstrable.

**Why.** A2's ordering is only meaningful if tiers are computed correctly; A5's analysis needs
speaking-time variance and starvation metrics.

**Implementation notes.**
- Define a deterministic tier rule: moderator = 1; participants below the fairness target = 2; the
  rest = 3. Publish the rule in `PROJECT.md`.
- Source speaking time from the exact ledger; refresh at a sensible cadence (not per frame).
- Include a starvation guard (e.g., escalate the longest-waiting participant) and document it.
- Expose per-participant speaking time, wait time, wins, and losses to the metrics layer.
- Keep the rule pure and unit-testable; no DB access inside the rule.

**Testing.**
- Property tests: lower tier always wins over higher tier; equal tiers fall through to timestamp
  then user id.
- Fairness scenario: speaking-time variance decreases versus random assignment.
- Starvation test: no participant exceeds the configured max wait.
- Determinism: same history → same tier assignment.

**Done when.** Tier assignment is deterministic, documented, and measurably fair; starvation is
bounded.

---

## US-017 — Participant metrics and local artifacts

**Priority:** P1 · **Depends on:** US-004, US-011

**Story.** As an analyst, I want each participant to record per-stage latency and counters locally,
so that run data survives even if the stats tables are unavailable.

**Why.** The TUI's 1 Hz snapshots are not sufficient for percentiles; the raw data must be captured
where it is produced.

**Implementation notes.**
- New `internal/metrics` additions: histograms for batch wait, DB write, claim verification read,
  acquisition, heartbeat, and snapshot write; counters for generated/acked/failed/retried/dropped.
- Write NDJSON samples or periodic summaries under `runs/<run_id>/client-<id>.ndjson`; flush on
  shutdown.
- Keep the hot path allocation-light: preallocate, reuse buffers, avoid per-event logging.
- Produce a final summary (p50/p95/p99, totals, error classes) for `bench_clients.summary`.
- Include environment and scenario metadata in the manifest, not in every sample.

**Testing.**
- Histogram math: known samples produce known percentiles (including p50/p95/p99).
- Summary round-trip: JSON schema is stable and versioned.
- Artifact flush on cancellation and on normal end.
- Overhead check: enabling metrics does not materially change throughput.

**Done when.** Every run produces a manifest, per-client raw data, and a final summary; percentiles
are computed from raw data.

---

## US-018 — Test modes and chaos hooks

**Priority:** P1 · **Depends on:** US-010, US-013, US-015

**Story.** As a lab operator, I want named modes that reproduce each adversarial condition, so that
every assumption is demonstrated deterministically.

**Why.** The project is a study; scenarios must be repeatable and clearly labeled (see `PROJECT.md`
§9).

**Implementation notes.**
- New `internal/chaos` with application-level hooks: clock offset, forced identical `claim_ts`,
  injected write timeout, injected delay/loss, and a "node down" mode driven externally.
- Modes: `random`, `races`, `fairness`, `skew`, `collision`, `timeout`, `audit`, `failure`, `scale`.
- Hooks are inert unless the mode enables them; record every active hook and its parameters in the
  manifest.
- Do not patch the OS clock; shift the timestamps the client generates.
- Ensure the hook cannot leak into other runs in the same process (per-run configuration).

**Testing.**
- Each mode is deterministic with a seed.
- Hooks off by default; enabling one changes only the intended dimension.
- Integration: `collision` produces equal timestamps; `timeout` produces the expected error class.
- No hook affects the control plane or unrelated runs.

**Done when.** Every mode in `PROJECT.md` §9 is runnable, labeled, and reproducible.

---

# Epic 2 — Agent control plane

## US-020 — HTTP control API and run registry

**Priority:** P1 · **Depends on:** US-005, US-010

**Story.** As an operator, I want to start, inspect, and cancel runs over HTTP without blocking, so
that one agent can drive multiple tests on a lab machine.

**Why.** The agent replaces the old "backend" role: it is a control plane, not a data proxy. Async
start and an explicit registry are what make remote operation safe.

**Implementation notes.**
- New `internal/agent`: routes `POST/GET/DELETE /v1/runs`, plus `/healthz` and `/metrics`.
- `POST` validates, registers, returns `202` with run/client ids; it must not wait for completion.
- Idempotency: the same `(run_id, client_id)` must not start twice; return `409` with the existing
  status.
- The registry is guarded; every run has its own context and cancel function.
- Return structured JSON errors with stable codes; do not leak stack traces.
- Enforce request body size limits and unknown-field rejection if practical.

**Testing.**
- `httptest`: valid start, duplicate start, invalid payload, unknown run, cancel, list.
- Concurrency: many simultaneous starts; no duplicate registration; race detector.
- API returns immediately (assert it does not block for the run duration).

**Done when.** Runs can be started, listed, inspected, and cancelled; duplicates are rejected; the
handler never blocks on the workload.

---

## US-021 — Run lifecycle, leases, and graceful shutdown

**Priority:** P1 · **Depends on:** US-020

**Story.** As an operator, I want every background run to end, report, and be detectable if the agent
dies, so that no test leaks forever.

**Why.** Background work plus persistence without lifecycle management produces zombie runs and
misleading status tables.

**Implementation notes.**
- Implement the state machine from `PROJECT.md` §6.8: scheduled → running → done/failed/cancelled,
  with stale detection.
- Require `duration` or `max_events`; validate at admission.
- Update `bench_clients.last_seen` on a lease interval; a run with no update beyond a threshold is
  stale.
- On shutdown: stop accepting, cancel runs, flush final summaries, close the session, exit.
- On failure, record the error class and transition to `failed`; never leave `running` behind.
- Keep finalization idempotent so a retried final write is safe.

**Testing.**
- Lifecycle transitions for success, cancellation, timeout, and injected failure.
- Stale detection: stop the agent mid-run; a reader sees `stale`.
- SIGTERM during a run: clean cancellation, flushed summary, bounded exit time.
- Goroutine leak check after many runs.

**Done when.** No run can outlive its duration or the agent; status is always terminal or stale;
shutdown is clean.

---

## US-022 — Admission control and resource caps

**Priority:** P1 · **Depends on:** US-020

**Story.** As an operator, I want the agent to reject unreasonable runs explicitly, so that a typo
cannot OOM the machine or distort another test.

**Why.** "No artificial parameter constraints" means the scenario space is open, not that resource
use is unbounded.

**Implementation notes.**
- Configurable caps: max concurrent runs, max participants per run, max total goroutines, max batch
  bytes, max queue depth.
- Reject with `422` for invalid parameters and `429`/`503` for capacity, including a clear message.
- Do not silently clamp; clamping makes results incomparable.
- Record the caps and active concurrency in the run manifest.
- Apply backpressure to telemetry writers when the queue is full; drop and count rather than grow.

**Testing.**
- Boundary tests for each cap.
- Overload: many starts; some rejected; no OOM; agent remains responsive.
- Verify a rejected run leaves no partial state.

**Done when.** Caps are enforced, errors are explicit, and overload is measurable.

---

## US-023 — Multi-agent coordination and orchestrator

**Priority:** P2 · **Depends on:** US-020, US-021

**Story.** As a benchmark operator, I want to start one logical run across several agents at the same
time, so that parallel stress tests share an id and a start time.

**Why.** Parallel tests are a core goal; independent per-agent ids would make the TUI and analysis
treat one experiment as many.

**Implementation notes.**
- Accept caller-provided `run_id`, `client_id`, and `start_at`; generate only when absent.
- `start_at` requires synchronized clocks (NTP); record measured offsets.
- `orchestrator` reads a target list, sends the same request to each agent, retries idempotently, and
  reports partial failures.
- One logical run has many `bench_clients` rows; the TUI merges by `run_id`.
- Handle a late agent: either start it late (record the offset) or reject it; decide and document.

**Testing.**
- Two agents, one run id; TUI/report sees one run with two clients.
- Synchronized start within a bounded tolerance.
- One agent unreachable: others proceed; the orchestrator reports the failure.
- Duplicate fan-out is idempotent.

**Done when.** One run id spans multiple agents; start offsets are recorded; partial failure is
visible.

---

## US-024 — Agent security and operational endpoints

**Priority:** P2 · **Depends on:** US-020

**Story.** As a lab operator, I want the control endpoint to be safe by default, so that an
accidental open port cannot start load on the cluster.

**Why.** The API starts writes to Cassandra; an unauthenticated, world-bound endpoint is a footgun
even in a lab.

**Implementation notes.**
- Bind to localhost by default; require an explicit listen address to expose.
- Optional bearer token for `/v1/*`; constant-time comparison; never log the token.
- Keep `/healthz` lightweight; decide whether it requires auth.
- Disable pprof in lab mode; enable only with an explicit development flag.
- Validate and normalize all inputs; no identifier interpolation into CQL.
- Add request rate limiting on run creation.

**Testing.**
- Default bind is loopback.
- Missing/invalid token is rejected when enabled.
- pprof is unavailable by default.
- Health check does not require Cassandra writes.

**Done when.** The endpoint is safe by default and operable.

---

# Epic 3 — Statistics and observer

## US-030 — Benchmark schema and payload versioning

**Priority:** P1 · **Depends on:** US-002

**Story.** As a developer, I want the `bench_*` tables and a versioned JSON payload, so that the TUI
and analysis can evolve independently of the agents.

**Why.** The TUI must read a stable contract; snapshots must not grow into unreadable blobs.

**Implementation notes.**
- Add the four tables from `PROJECT.md` §5.3 as a new migration: `bench_runs`, `bench_clients`,
  `bench_snapshots`, `bench_latest`.
- Per-client keys `(run_id, client_id)`; never a single growing blob per run.
- Put `schema_version` in each payload and in `bench_clients`; define a decoder that tolerates
  unknown fields.
- TTLs: snapshots 24 h, latest 7 d, run metadata retained longer.
- Document the payload schema (fields, units, nullability) next to the tables.

**Testing.**
- Migration applies and re-applies cleanly.
- Write/read latest; history order is descending; TTL behavior verified.
- Unknown payload version is handled gracefully by the TUI/report.
- Multi-client rows do not overwrite each other.

**Done when.** The schema exists, is versioned, and the read patterns from `PROJECT.md` §5.4 work.

---

## US-031 — Client snapshot push

**Priority:** P1 · **Depends on:** US-017, US-030

**Story.** As a client runtime, I want to push a small JSON snapshot from my in-memory metrics, so
that the TUI is fresh without querying the hot telemetry tables.

**Why.** Reading telemetry to render a dashboard would add load to the system under test and would
still miss client-internal state.

**Implementation notes.**
- Snapshot at 1–5 Hz from in-memory counters; include run/client identity, timestamp, rates,
  latency histograms, queue depths, claim state, and error classes.
- Upsert `bench_latest` every tick and insert into `bench_snapshots` (subject to TTL).
- Keep the payload small and bounded; if it grows, split into sections rather than one giant blob.
- Best-effort: a failed snapshot must not fail the run; skip and count.
- Use a separate low-priority context so shutdown can finish the final snapshot.

**Testing.**
- Cadence accuracy; latest row matches the most recent tick.
- History ordering; TTL expiry.
- Cassandra unavailable: run continues, skips are counted.
- Payload schema validation and size bound.

**Done when.** The TUI can render a run from `bench_latest` alone; snapshot failures are invisible
to the data path.

---

## US-032 — Observer: domain state pull

**Priority:** P1 · **Depends on:** US-030, US-013, US-014

**Story.** As an observer, I want to sample the authoritative floor state from Cassandra, so that
the TUI shows the cluster's view, not one client's guess.

**Why.** With clients writing directly, room state is shared; the observer must read it rather than
trust a local process.

**Implementation notes.**
- Pull at 1–2 Hz: top claims per room, `room_active_state`, heartbeat liveness, queue length, and
  counter totals.
- Bound every query: per-room top-K, no scans, short timeouts, `CL=ONE` for observation.
- Merge into the same snapshot payload written by the client, or write an observer row keyed by
  `(run_id, "observer")`; choose one and document it.
- Skip a tick on error; never block the run.
- Record the observer's own query count and latency.

**Testing.**
- Fake store unit tests for merge logic.
- Integration: claims written by a client appear in the next observer tick.
- Error injection: a slow/failed query skips without stalling.
- Load: observer overhead measured at different room counts.

**Done when.** The TUI sees cross-client floor state with bounded overhead.

---

## US-033 — Observer: cluster diagnostics

**Priority:** P2 · **Depends on:** US-032

**Story.** As an analyst, I want periodic Cassandra health samples, so that application latency can
be attributed to the cluster.

**Why.** The lab's conclusions depend on distinguishing client, network, and database effects.

**Implementation notes.**
- Sample at ≤1 Hz: thread pools, pending compactions, coordinator/local latency histograms, client
  connections, table estimates, GC where available.
- Cassandra 4.1 `system_views` virtual tables are node-local; document that a normal session sees
  only the coordinator's node. For per-node data, use per-node sessions or JMX/Prometheus.
- Use a separate session/config with short timeouts and low consistency; best-effort only.
- Record skipped ticks and the query cost in the manifest.
- Never scan application tables for diagnostics.

**Testing.**
- Each diagnostic query is verified against the running cluster.
- Node-local limitation documented and, if needed, demonstrated.
- Failure of any diagnostic does not affect the run.
- Overhead measured as a function of sampling rate.

**Done when.** Cluster health is captured with bounded overhead and clear limitations.

---

## US-034 — Run metadata and final results

**Priority:** P1 · **Depends on:** US-017, US-030

**Story.** As an analyst, I want a final per-client summary plus run metadata, so that runs are
comparable and self-describing.

**Why.** Percentiles cannot be recovered from 1 Hz snapshots; comparison requires the scenario,
commit, and environment.

**Implementation notes.**
- `bench_runs`: scenario JSON, git commit, schema version, start/end, status.
- `bench_clients`: final counters, error classes, summary JSON with p50/p95/p99 and per-stage
  latency, host, environment, clock offset, concurrency.
- Write once at the end, idempotently; retry safely.
- Capture environment: CPU count, Go version, kernel, Cassandra topology/heap, container limits.
- Keep the manifest in `runs/<run_id>/manifest.json` as the local copy.

**Testing.**
- Final summary equals the local artifact.
- Missing fields fail validation rather than writing zeros silently.
- Retried final write does not duplicate or corrupt.
- Comparison of two runs uses only `bench_*` data and produces the same percentiles as local.

**Done when.** Every run is self-describing and comparable from the tables alone.

---

# Epic 4 — TUI

## US-040 — TUI data layer

**Priority:** P2 · **Depends on:** US-030, US-031

**Story.** As a viewer, I want the TUI to read one run's latest snapshots and merge clients, so that
rendering is cheap and independent of the data path.

**Why.** The TUI is a pure consumer; it must not become a second load source or require a live API.

**Implementation notes.**
- New `internal/tui/fetch`: read `bench_latest` by `run_id`; merge per-client payloads; tolerate a
  client appearing/disappearing.
- Validate `schema_version`; render known fields and ignore unknown ones.
- Fetch at 4–10 Hz; back off on errors; never query per panel or per frame.
- Accept a run selector; default to the most recent run.
- Ensure the DB session used by the TUI is separate from any agent session.

**Testing.**
- Fake reader: single client, multiple clients, missing client, stale client.
- Version mismatch is handled without a crash.
- Fetch errors show a clear state and retry.
- No per-render query (instrument the reader).

**Done when.** One query per run per refresh; multi-client merge works; no API dependency.

---

## US-041 — TUI layout and panels

**Priority:** P2 · **Depends on:** US-040

**Story.** As a viewer, I want a readable dashboard of the run, so that I can see the system state at
a glance during the lab.

**Why.** "Real-time TUI" is a stated requirement and a demonstration aid.

**Implementation notes.**
- Panels: run status, active speaker per room, waiting queue, noise floor, per-room counters,
  ingest rate, error/timeout counts, cluster health, and a stale/failed banner.
- Use the planned TUI library; keep rendering pure from a view model.
- Decouple data refresh (4–10 Hz) from render (30–60 Hz); do not block rendering on I/O.
- Handle small terminals; degrade panels rather than overflow.
- Color is supplementary, never the only signal.

**Testing.**
- Golden render tests for representative view models.
- Resize tests at small/medium/large sizes.
- No-data and error states render deterministically.
- A slow reader does not freeze input handling.

**Done when.** The dashboard is readable, responsive, and decoupled from fetch cadence.

---

## US-042 — TUI states, history, and run selection

**Priority:** P2 · **Depends on:** US-041

**Story.** As a viewer, I want to inspect recent history and switch runs, so that I can diagnose a
test rather than only watch the present.

**Why.** Operational value comes from seeing stale/failed runs and recent trends.

**Implementation notes.**
- State handling: scheduled, running, done, failed, cancelled, stale, no data.
- History from `bench_snapshots` with a bounded window; sparklines or simple tables, not raw dumps.
- Run selector across `bench_runs`; show scenario summary and git commit.
- Clear indication when the data is older than a freshness threshold.
- Graceful exit on `q`/Ctrl-C without leaving the terminal in a bad state.

**Testing.**
- State transition table rendered correctly.
- History window bounded; no unbounded memory.
- Switching runs resets state cleanly.
- Terminal restored after exit.

**Done when.** The TUI can explain what happened, not just what is happening.

---

# Epic 5 — Auditor and reporting

## US-050 — Token-range paginator with deduplication

**Priority:** P1 · **Depends on:** US-003

**Story.** As an analyst, I want to scan `audio_telemetry` by token ranges and deduplicate boundary
rows, so that A6's exact-count claim is demonstrated.

**Why.** `COUNT(*)` is unreliable at scale and token paging duplicates rows at boundaries; the
project intends to show the correct technique.

**Implementation notes.**
- New `internal/auditor`: enumerate token ranges, page with a stable size, and resume from a token.
- Maintain a seen-set keyed by the telemetry primary key; bound its memory and report it.
- Count per room/bucket and total; compare with the run's generated/acked ledger.
- Support a max-row/max-time budget; stop cleanly and label the result partial.
- Stream results; never load all rows into memory.

**Testing.**
- Synthetic boundary duplicates are removed; count matches a known fixture.
- Memory stays within the configured bound.
- Cancellation and resume work.
- Exactness within budget; partial result is labeled.

**Done when.** The auditor produces a deduplicated count with documented time/memory bounds.

---

## US-051 — Audit report and exactness envelope

**Priority:** P1 · **Depends on:** US-050, US-034

**Story.** As an analyst, I want an audit report comparing generated, acked, and stored frames, so
that "no loss" is a measured claim.

**Why.** The stress test's central assertion is that sent events equal stored events; it must be
provable and bounded.

**Implementation notes.**
- Compare three sources: client-generated counter, client-acked counter, deduped stored count.
- Report missing, extra, and duplicate counts per room/bucket.
- State the envelope: rows scanned, memory, wall-clock, whether the scan finished inside the TTL.
- If the run exceeds the envelope, switch to ingestion-time aggregates and label the result
  approximate.
- Produce both machine-readable and human-readable output.

**Testing.**
- Fixtures with known missing/extra rows produce the expected deltas.
- Boundary duplicates are counted and removed.
- Envelope exceeded → partial/approximate label.
- Report is deterministic for a fixed fixture.

**Done when.** The report proves or disproves loss with stated limits.

---

## US-052 — `ALLOW FILTERING` negative demonstration

**Priority:** P2 · **Depends on:** US-003

**Story.** As a lab presenter, I want to show the cost and rejection of a non-key query, so that the
query-driven schema argument is concrete.

**Why.** A6 requires demonstrating why the lookup table exists.

**Implementation notes.**
- Add a deliberately non-key query (e.g., telemetry by `user_id` without `room_id`) and capture the
  failure without `ALLOW FILTERING`.
- Provide an explicitly labeled diagnostic path that runs it with filtering, measures latency and
  scanned rows, and never ships in a domain path.
- Record the result in the experiment artifacts.
- Keep it behind a command/mode so it cannot be invoked accidentally by production code.

**Testing.**
- Without filtering: the query is rejected (or a full scan is demonstrated where allowed).
- With filtering: latency is measured and recorded.
- Static check: no `ALLOW FILTERING` outside the labeled demo.

**Done when.** The negative demo is reproducible and isolated.

---

## US-053 — Denormalized `active_room_by_user` lookup

**Priority:** P1 · **Depends on:** US-013, US-015

**Story.** As a moderator, I want to ask "which room is user X in?" without filtering, so that A6's
denormalization solution works.

**Why.** The base schema cannot answer this efficiently; the lookup table is the correct Cassandra
approach.

**Implementation notes.**
- Update `active_room_by_user` on claim/release with a logged batch alongside the claim mutation.
- Accept and document that a cross-partition logged batch is not isolated and is expensive; the
  correctness story is eventual consistency plus reconciliation.
- On read, if the row is missing or stale, reconcile from `floor_claims`.
- Handle concurrent room changes for the same user; last-write-wins is acceptable if documented.

**Testing.**
- Lookup correctness after claim/release.
- Batch failure injection: reconciliation repairs the lookup.
- Concurrent updates do not leave a permanently wrong row.
- No `ALLOW FILTERING` is used.

**Done when.** The lookup answers the moderator query correctly with bounded cost.

---

## US-054 — `report` command and run comparison

**Priority:** P1 · **Depends on:** US-034

**Story.** As an analyst, I want an offline report over one or many runs, so that I can compare
scenarios and extract evidence per assumption.

**Why.** The deliverable is analysis, not just execution.

**Implementation notes.**
- New `internal/report`: read `bench_runs`/`bench_clients` (or local artifacts), aggregate by run,
  scenario, and client.
- Output: p50/p95/p99, throughput, error classes, retry counts, correctness metrics, and a CSV/JSON
  export.
- Support side-by-side comparison of two runs (naive vs corrected, baseline vs stress).
- Golden-test the output format; keep units explicit.
- Refuse to compare runs with incompatible schema versions without a warning.

**Testing.**
- Golden reports for fixed inputs.
- Multi-client merge correctness.
- Missing data produces a clear warning, not zeros.
- Comparison identifies the expected differences.

**Done when.** A run's evidence can be produced offline and compared reproducibly.

---

# Epic 6 — Lab scenarios

> Each story turns one assumption into a repeatable experiment with an oracle. Use the mode from
> `PROJECT.md` §9 and record the result under `runs/<run_id>/report.md`.

## US-060 — Stress stream: no loss under sustained load

**Priority:** P1 · **Depends on:** US-011, US-050

**Story.** As a presenter, I want a sustained 100 Hz stream for a fixed duration, so that I can show
sent == stored and the error taxonomy.

**Why.** This is the baseline A1 demonstration and the foundation for every other measurement.

**Implementation notes.** Start at the defaults (4 rooms × 10 users); sweep upward until the
measured knee. Record generated/acked/stored, error classes, queue depth, and Cassandra histograms.
Run long enough to exercise compaction and TTL. Do not silently retry into a different workload.

**Testing.** Fixed seed; repeated runs; audit within the envelope; assert flat memory and bounded
goroutines.

**Done when.** The run produces a complete report and a saturation point with evidence.

---

## US-061 — Race: exactly one winner

**Priority:** P1 · **Depends on:** US-013, US-018

**Story.** As a presenter, I want synchronized onsets within 2–15 ms, so that I can show one winner
per turn without LWT.

**Why.** A2 is the central correctness claim.

**Implementation notes.** Use the `races` mode with a barrier or a shared `start_at`; record each
contender's claim, read order, and outcome. Assert index 0 uniqueness per turn. Repeat many rounds
and report the distribution.

**Testing.** Property test on the ordering; integration repeats; oracle `multiple_winners == 0`.

**Done when.** Repeated runs show exactly one winner and a correct queue, with no LWT.

---

## US-062 — Clock skew: no deadlock, no DELETE

**Priority:** P1 · **Depends on:** US-014, US-018

**Story.** As a presenter, I want ±60 s client clock offsets, so that I can show the future-tombstone
deadlock is avoided.

**Why.** A3 is the most distinctive Cassandra workaround in the project.

**Implementation notes.** Use `--clock-offset` per participant; run claim/release/next-claim cycles;
assert the next turn proceeds, heartbeats expire, and no DELETE exists. Also record intra-turn
ordering anomalies as a known limitation.

**Testing.** Static check for DELETE; scenario run with both positive and negative offsets; stale
detection.

**Done when.** The scenario completes without deadlock and the evidence is recorded.

---

## US-063 — Torn rows: naive vs corrected

**Priority:** P1 · **Depends on:** US-013, US-018

**Story.** As a presenter, I want forced timestamp collisions, so that I can show invariant
violations in the naive table and none in the corrected one.

**Why.** A4's comparison is only credible if both tables receive the same problematic workload.

**Implementation notes.** `collision` mode forces identical `claim_ts` across writers; write both
tables; check `noise + inverse == 0` and JSON consistency. Report violation rates side by side.
Ensure the collision is injected, not accidental.

**Testing.** Deterministic collision fixture; oracle violation rate > 0 naive, == 0 corrected;
repeatability.

**Done when.** The comparison is reproducible and quantified.

---

## US-064 — Counter drift under timeouts

**Priority:** P1 · **Depends on:** US-015, US-018

**Story.** As a presenter, I want injected write timeouts, so that I can show counter overcount and
the exact ledger alternative.

**Why.** A5 is a correctness claim about non-idempotent operations.

**Implementation notes.** `timeout` mode injects failures at a controlled rate; run with and without
blind retries; compare counters to the ledger. Report drift, retry counts, and error classes. Do not
hide the drift; it is the point.

**Testing.** Controlled timeout fixture; ledger exact; counter drift measurable and repeatable.

**Done when.** The drift is demonstrated and explained.

---

## US-065 — Audit: page-boundary duplicates

**Priority:** P1 · **Depends on:** US-050, US-051

**Story.** As a presenter, I want a token scan with and without dedupe, so that I can show boundary
duplication and the correction.

**Why.** A6's pagination claim must be demonstrated, not asserted.

**Implementation notes.** Use a fixed dataset with known partition boundaries; run the paginator
with dedupe disabled (diagnostic) and enabled; compare counts. Report duplicate rate, time, memory,
and exactness envelope.

**Testing.** Synthetic fixture; expected duplicate count; deduped count exact; envelope labels.

**Done when.** The duplication and its correction are measured.

---

## US-066 — Failure and recovery

**Priority:** P2 · **Depends on:** US-003, US-021

**Story.** As a presenter, I want a node failure during a run, so that I can show QUORUM behavior,
retries, and recovery.

**Why.** Correctness under failure is part of the analysis; RF=3 exists for this.

**Implementation notes.** Stop one Cassandra node mid-run; measure error rate, retry amplification,
latency, and recovery time. Record Cassandra's view (`tpstats`, histograms) and the client's view.
Distinguish retryable timeouts from unavailability.

**Testing.** Repeatable stop/start; no run corruption; bounded retries; clear recovery marker.

**Done when.** Failure behavior is measured and explained.

---

## US-067 — Fairness and starvation

**Priority:** P1 · **Depends on:** US-016, US-018

**Story.** As a presenter, I want a skewed speaking history, so that I can show priority ordering and
bounded starvation.

**Why.** A5's motivational claim is fairness; it needs evidence.

**Implementation notes.** `fairness` mode assigns skewed histories; run long enough to observe
turn-taking; report tier distribution, speaking-time variance, max wait, and wins by tier.

**Testing.** Property tests on tier ordering; scenario repeatability; starvation bound.

**Done when.** Fairness metrics are produced and the bound holds.

---

## US-068 — Scale and partition sizing

**Priority:** P2 · **Depends on:** US-011, US-033

**Story.** As a presenter, I want a users-per-room sweep, so that I can show partition growth and the
point where the guidance is exceeded.

**Why.** A1's real limit is bytes per partition, not cell count; the report must show the curve.

**Implementation notes.** Sweep users/room at fixed frequency; record `nodetool tablestats`
partition sizes, compaction backlog, GC, and latency. Identify the knee. Keep the audit within its
envelope or label it partial.

**Testing.** Fixed seeds; environment recorded; runs comparable.

**Done when.** The partition-size curve and the operating limit are documented.

---

## US-069 — Collections: list and map behavior

**Priority:** P3 · **Depends on:** US-012

**Story.** As a presenter, I want to demonstrate collection read-modify-write behavior, so that the
`list`/`map` choices are justified.

**Why.** A6 includes collections; their concurrency behavior is a common misuse.

**Implementation notes.** Update `recent_noise_samples` and `device_metadata` under concurrent
writers; show the overwrite/lost-update behavior; compare with a separate-row history table.

**Testing.** Concurrent update fixture; lost-update rate; bounded list.

**Done when.** The collection trade-off is demonstrated and documented.

---

# Epic 7 — Benchmark harness and analysis

## US-070 — Scenario matrix and seeds

**Priority:** P2 · **Depends on:** US-010, US-018

**Story.** As an analyst, I want a versioned scenario matrix with fixed seeds, so that every
experiment is reproducible.

**Why.** Comparisons are only valid when the workload is identical and recorded.

**Implementation notes.** Maintain a set of scenario files per assumption (baseline, race, skew,
collision, timeout, audit, failure, scale). Include seed, duration, consistency, batching, and floor
parameters. Version the files; record them verbatim in the manifest.

**Testing.** Every scenario file validates; a dry run prints the effective config; two runs with the
same file and seed match.

**Done when.** The matrix is complete, versioned, and used by the lab runs.

---

## US-071 — Open-loop and closed-loop generation

**Priority:** P2 · **Depends on:** US-010

**Story.** As a performance analyst, I want both generation modes, so that tail latency is measured
without coordinated omission.

**Why.** Closed-loop generators under-report overload latency; the analysis must be honest about
which was used.

**Implementation notes.** Open-loop: fixed arrival rate independent of completion. Closed-loop: fixed
concurrency waiting for acknowledgements. Record the mode and the effective achieved rate. Provide a
way to detect when the generator cannot keep up.

**Testing.** Achieved rate matches requested within tolerance; overload is visible; mode recorded.

**Done when.** Both modes exist and are labeled in every artifact.

---

## US-072 — Artifact manifest and environment capture

**Priority:** P2 · **Depends on:** US-017, US-034

**Story.** As an analyst, I want every run to carry a manifest with environment and configuration, so
that results are comparable and defensible.

**Why.** A number without its environment is an anecdote.

**Implementation notes.** Capture git commit, Go version, kernel, CPU count, container limits,
Cassandra topology/heap, network path (loopback vs LAN), clock offsets, observer overhead, and all
scenario parameters. Write both locally and to `bench_runs`.

**Testing.** Manifest schema validation; missing environment fields fail; two runs diff cleanly.

**Done when.** Every run is self-describing.

---

## US-073 — Orchestrated multi-machine run

**Priority:** P2 · **Depends on:** US-023, US-070

**Story.** As an operator, I want one command to start the same scenario on several agents, so that
I can produce a parallel stress test without manual coordination.

**Why.** This is the practical payoff of the agent architecture.

**Implementation notes.** Read a target list; generate one `run_id`; schedule a common `start_at`;
fan out idempotently; poll status; collect per-client results; report partial failures. Record start
offsets and clock offsets.

**Testing.** Two-machine run produces one logical run; one machine offline is reported; retries are
idempotent; teardown is clean.

**Done when.** A multi-machine run is one command and one run id.

---

## US-074 — Analysis templates and report structure

**Priority:** P2 · **Depends on:** US-054

**Story.** As an author, I want a standard analysis structure per assumption, so that the final report
is consistent and complete.

**Why.** The project's value is the analysis; a template prevents missing evidence.

**Implementation notes.** Per assumption: hypothesis, workload, correctness oracle, measured result,
interpretation, limitations/threats to validity. Include the naive-vs-corrected comparisons and the
failure envelope. Keep raw artifacts linked by run id.

**Testing.** The template is applied to at least one completed run per assumption; no section is
empty.

**Done when.** The analysis is reproducible from artifacts and follows the template.

---

## US-075 — Continuous benchmarks and regression checks

**Priority:** P3 · **Depends on:** US-054, US-072

**Story.** As a developer, I want a repeatable benchmark job that flags regressions, so that
performance does not silently decay.

**Why.** Performance is a property that regresses; the project should demonstrate measurement
discipline.

**Implementation notes.** Microbenchmarks with repetitions and `benchstat`; a nightly integration run
at a fixed rate; thresholds with noise awareness; store results with commit and environment. Do not
gate on noisy single samples.

**Testing.** The job runs on a clean cluster; a deliberate regression is detected; noise does not
cause flaky failures.

**Done when.** Regressions are visible before the defense.

---

# Epic 8 — Documentation and defense

## US-080 — Align README and historical notes with the source of truth

**Priority:** P1 · **Depends on:** US-005

**Story.** As a new developer, I want the README to describe the current architecture, so that I do
not follow stale instructions.

**Why.** `README.md` and `ASSUMPTIONS.md` currently describe roles and schemas that no longer match.

**Implementation notes.** Point to `PROJECT.md` as canonical; update the command list, roles,
topology, and quick start; mark `ASSUMPTIONS.md` as historical. Keep the README short and operational.

**Testing.** Follow the README from a clean checkout; every command works.

**Done when.** A new developer can build and run from the README without guessing.

---

## US-081 — Lab runbook

**Priority:** P2 · **Depends on:** US-073

**Story.** As an operator, I want a step-by-step runbook, so that the lab demo is repeatable under
time pressure.

**Why.** Cluster startup, migration, agent rollout, scenario order, and teardown are easy to get
wrong live.

**Implementation notes.** Cover: compose up/wait, migrate, start agents, verify health, run each
scenario, collect artifacts, inspect the TUI, tear down. Include expected durations and failure
recovery. List the exact commands and the order.

**Testing.** Dry-run the runbook end to end on a clean machine.

**Done when.** The demo can be executed from the document alone.

---

## US-082 — Demo script and expected observations

**Priority:** P2 · **Depends on:** US-060…069

**Story.** As a presenter, I want a script that maps each scenario to the claim it proves and the
expected observation, so that the defense is coherent.

**Why.** Six assumptions need a narrative, not six unrelated tests.

**Implementation notes.** For each assumption: setup, command, expected output, what to point at in
the TUI, and the limitation to state honestly. Include the naive-vs-corrected contrast and the
failure envelope.

**Testing.** Rehearse against a clean cluster; timings fit the defense slot.

**Done when.** The defense narrative is complete and rehearsed.

---

## US-083 — Results report template and threats to validity

**Priority:** P2 · **Depends on:** US-074

**Story.** As an author, I want a report template with explicit threats to validity, so that the
analysis is honest and defensible.

**Why.** Loopback vs LAN, 128 MB heaps, small sample sizes, and coordinated omission can invalidate
conclusions if unstated.

**Implementation notes.** Sections: environment, workload, results, correctness, limitations,
reproducibility. Call out: heap starvation, loopback measurements, single-DC/RF3, observer overhead,
exactness envelope, clock-skew scope, and concurrent-run interference.

**Testing.** Review against completed runs; every limitation has evidence or a stated bound.

**Done when.** The report can be defended without hand-waving.

---

# Cross-cutting checklist

Use this before declaring the project "implemented":

- [ ] `migrate` is the only schema writer; agents verify the version.
- [ ] One gocql session per process; token-aware; idempotency-aware retries.
- [ ] No LWT, no DELETE on floor state, no ALLOW FILTERING in domain paths.
- [ ] Telemetry batched, bucketed in UTC, TTL-bound, drop-counted.
- [ ] Claim state machine is isolated, cancellable, and property-tested for one winner.
- [ ] Heartbeats are TTL-based; release never deletes; stale speakers are detected.
- [ ] Counters aggregated; ledger exact; drift demonstrated under timeouts.
- [ ] Agent API is async, idempotent, capped, and cancellable.
- [ ] Runs have ids, durations, leases, final summaries, and manifests.
- [ ] Snapshots are 1–5 Hz from memory; TUI reads only `bench_latest`/`bench_snapshots`.
- [ ] Observer pull is bounded, best-effort, and its overhead is recorded.
- [ ] Auditor deduplicates and states its exactness envelope.
- [ ] Every assumption has a deterministic scenario and a correctness oracle.
- [ ] Report and TUI have golden tests; metrics have histograms, not averages.
- [ ] README, PROJECT.md, and the lab runbook agree.
