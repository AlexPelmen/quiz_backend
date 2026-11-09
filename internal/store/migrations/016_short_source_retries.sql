-- Переводим прежний интервал по умолчанию на короткие повторы.
UPDATE settings SET value = value || '{"source_429_delay_seconds":1,"source_429_max_delay_seconds":3}'::jsonb
WHERE value->>'source_429_delay_seconds' = '30' AND value->>'source_429_max_delay_seconds' = '300';
