CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX artists_answer_prefix ON artists (lower(name) text_pattern_ops);
CREATE INDEX albums_answer_prefix ON albums (lower(name) text_pattern_ops);
CREATE INDEX tracks_answer_prefix ON tracks (lower(name) text_pattern_ops);
CREATE INDEX artists_answer_trgm ON artists USING gin (lower(name) public.gin_trgm_ops);
CREATE INDEX albums_answer_trgm ON albums USING gin (lower(name) public.gin_trgm_ops);
CREATE INDEX tracks_answer_trgm ON tracks USING gin (lower(name) public.gin_trgm_ops);

CREATE TABLE quiz_question_answers (
    question_id uuid NOT NULL REFERENCES quiz_questions(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    attempt_id uuid NOT NULL,
    answer_text text NOT NULL CHECK (answer_text <> ''),
    correct boolean NOT NULL,
    PRIMARY KEY (question_id, user_id, attempt_id)
);
