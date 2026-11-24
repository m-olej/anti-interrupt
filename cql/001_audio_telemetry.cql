CREATE TABLE audio_telemetry (
    room_id text,
    minute_bucket text,          -- Format: 'YYYY-MM-DDTHH:SS' UTC
    frame_ts timestamp,
    user_id text,
    event_category text,         -- 'A' | 'S' | 'T'
    PRIMARY KEY ((room_id, minute_bucket), frame_ts, user_id)
) WITH CLUSTERING ORDER BY (frame_ts DESC, user_id ASC);
