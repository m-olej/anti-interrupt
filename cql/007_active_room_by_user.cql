CREATE TABLE active_room_by_user (
  user_id  text PRIMARY KEY,
  room_id  text,
  turn_seq int,
  state    text,                -- SPEAKING | QUEUED | IDLE
  since    timestamp
);
