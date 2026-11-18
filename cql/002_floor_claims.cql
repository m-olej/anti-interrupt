CREATE TABLE floor_claims (
    room_id text,
    turn_seq int,                -- Monotonic logical turn  
    priority_tier int,           -- Calculated by speaking history of a session
    claim_ts bigint,             -- Microsecond timestamp
    user_id text,
    claim_status text,           -- 'CLAIMING', 'SPEAKING', 'QUEUED', 'RELEASED'
    PRIMARY KEY ((room_id), turn_seq, priority_tier, claim_ts, user_id)
) WITH CLUSTERING ORDER BY (turn_seq DESC, priority_tier ASC, claim_ts ASC, user_id ASC);
