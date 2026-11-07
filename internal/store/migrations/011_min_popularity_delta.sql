UPDATE settings SET value = value || '{"min_popularity_delta":1}'::jsonb
WHERE NOT (value ? 'min_popularity_delta');
