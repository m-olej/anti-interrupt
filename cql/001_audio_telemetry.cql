CREATE TABLE audio_telemetry (
    room_id text,
    minute_bucket text,          -- Format: '2026-11-03T16:05'
    frame_ts timestamp,
    user_id text,
    event_category text,
    PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)
) WITH CLUSTERING ORDER BY (frame_ts DESC, user_id ASC);
