CREATE TABLE completed_speech_turns (
    room_id text,
    user_id text,
    turn_id uuid,
    duration_ms int,
    PRIMARY KEY ((room_id, user_id), turn_id)
);
