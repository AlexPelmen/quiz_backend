UPDATE settings SET value = '{
  "genius_request_interval_ms":1000,
  "source_429_retries":3,
  "source_429_delay_seconds":30,
  "source_429_max_delay_seconds":300
}'::jsonb || value;
