-- Старые вопросы не содержат точных целей измерения известности.
DELETE FROM quiz_packs;

ALTER TABLE artists ALTER COLUMN points TYPE double precision,
    ADD COLUMN guess_num integer NOT NULL DEFAULT 0 CHECK (guess_num >= 0);
ALTER TABLE albums ALTER COLUMN points TYPE double precision,
    ADD COLUMN guess_num integer NOT NULL DEFAULT 0 CHECK (guess_num >= 0);
ALTER TABLE tracks ALTER COLUMN points TYPE double precision,
    ADD COLUMN guess_num integer NOT NULL DEFAULT 0 CHECK (guess_num >= 0);

ALTER TABLE quiz_questions ALTER COLUMN track_id DROP NOT NULL,
    ADD COLUMN album_id uuid REFERENCES albums(id),
    ADD CONSTRAINT quiz_question_target CHECK (num_nonnulls(track_id, album_id) <= 1
        AND (track_id IS NOT NULL) = (sound_download_id IS NOT NULL));

CREATE TABLE quiz_round_results (
    round_id uuid NOT NULL REFERENCES quiz_rounds(id) ON DELETE CASCADE,
    attempt_id uuid NOT NULL,
    r integer NOT NULL CHECK (r >= 0),
    n integer NOT NULL CHECK (n > 0 AND r <= n),
    answers boolean[] NOT NULL CHECK (cardinality(answers) = n),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (round_id, attempt_id)
);

UPDATE settings SET value = value || '{"k_popularity":20}'::jsonb
WHERE NOT (value ? 'k_popularity');
