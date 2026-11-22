ALTER TABLE quiz_packs
    ADD COLUMN status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'pending', 'ready')),
    ADD COLUMN clip_error text NOT NULL DEFAULT '';

ALTER TABLE quiz_questions
    ADD COLUMN status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'pending', 'ready')),
    ADD COLUMN clip_name text NOT NULL DEFAULT '',
    ADD COLUMN clip_lease_until timestamptz,
    ADD CHECK ((status='pending') = (clip_lease_until IS NOT NULL)),
    ADD CHECK ((clip_name<>'') = (status='ready' AND sound_download_id IS NOT NULL));

UPDATE quiz_questions SET status='ready' WHERE sound_download_id IS NULL;

UPDATE quiz_packs p SET status='ready' WHERE NOT EXISTS (
    SELECT 1 FROM quiz_rounds r JOIN quiz_questions q ON q.round_id=r.id
    WHERE r.pack_id=p.id AND q.status<>'ready'
);
