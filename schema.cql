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
    event_category text,
    PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)
) WITH CLUSTERING ORDER BY (frame_ts DESC, user_id ASC);

-- Lock-Free Floor Control Queue (Solves Mutual Exclusion without LWT)
CREATE TABLE floor_claims (
    room_id text,
    turn_seq int,                -- Monotonic logical turn  
    priority_tier int,           -- Calculated by speaking history of a session
    claim_ts bigint,             -- Microsecond timestamp
    user_id text,
    claim_status text,           -- 'CLAIMING', 'SPEAKING', 'QUEUED', 'RELEASED'
    PRIMARY KEY ((room_id), turn_seq, priority_tier, claim_ts, user_id)
) WITH CLUSTERING ORDER BY (turn_seq DESC, priority_tier ASC, claim_ts ASC, user_id ASC);

-- Idempotent Speech Turn Ledger (Survives Network Partitions with Idempotent Retries)
CREATE TABLE completed_speech_turns (
    room_id text,
    user_id text,
    turn_id uuid,
    duration_ms int,
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
