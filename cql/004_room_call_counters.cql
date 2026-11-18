CREATE TABLE room_call_counters (
    room_id text,
    user_id text,
    speech_frames_count counter,
    interruption_attempts counter,
    muted_by_arbiter_count counter,
    PRIMARY KEY ((room_id), user_id)
);
