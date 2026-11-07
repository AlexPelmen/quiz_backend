ALTER TABLE quiz_questions ADD COLUMN answer_info jsonb NOT NULL DEFAULT '{}'::jsonb;
UPDATE quiz_questions q SET answer_info = jsonb_build_object(
    'artist', a.name, 'track', t.name, 'album', COALESCE(al.name, '')
) FROM artists a, tracks t LEFT JOIN albums al ON al.id = t.album_id
WHERE a.id = q.artist_id AND t.id = q.track_id;
