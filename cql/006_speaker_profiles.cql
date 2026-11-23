CREATE TABLE speaker_profiles (
  room_id              text,
  user_id              text,
  base_noise_floor_db  double,          -- random mock
  recent_noise_samples list<double>,    -- keep last X
  device_metadata      map<text, text>, -- include hostname (legible during lab defense)
  PRIMARY KEY ((room_id), user_id)
);
