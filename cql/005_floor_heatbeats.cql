CREATE TABLE floor_heartbeats (
  room_id   text,
  turn_seq  int,
  user_id   text,
  seen_at   timestamp,
  PRIMARY KEY ((room_id), turn_seq, user_id)
) WITH default_time_to_live = 2;
