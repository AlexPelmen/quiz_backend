-- Снимаем диакритику с латинских букв, сохраняя, например, различие между и и й.
CREATE FUNCTION quiz_answer_fold(value text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
RETURN normalize(regexp_replace(
    normalize(value, NFD) COLLATE "C",
    U&'([A-Za-z])[\0300-\036F]+', E'\\1', 'g'
), NFC);

DROP INDEX artists_answer_prefix, albums_answer_prefix, tracks_answer_prefix,
    artists_answer_trgm, albums_answer_trgm, tracks_answer_trgm;

ALTER TABLE artists ADD COLUMN answer_search_name text GENERATED ALWAYS AS (lower(quiz_answer_fold(name))) STORED;
ALTER TABLE albums ADD COLUMN answer_search_name text GENERATED ALWAYS AS (lower(quiz_answer_fold(name))) STORED;
ALTER TABLE tracks ADD COLUMN answer_search_name text GENERATED ALWAYS AS (lower(quiz_answer_fold(name))) STORED;

CREATE INDEX artists_answer_prefix ON artists (answer_search_name text_pattern_ops);
CREATE INDEX albums_answer_prefix ON albums (answer_search_name text_pattern_ops);
CREATE INDEX tracks_answer_prefix ON tracks (answer_search_name text_pattern_ops);
CREATE INDEX artists_answer_trgm ON artists USING gin (answer_search_name public.gin_trgm_ops);
CREATE INDEX albums_answer_trgm ON albums USING gin (answer_search_name public.gin_trgm_ops);
CREATE INDEX tracks_answer_trgm ON tracks USING gin (answer_search_name public.gin_trgm_ops);
