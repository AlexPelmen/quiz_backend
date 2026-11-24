CREATE FUNCTION quiz_answer_search_key(value text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
RETURN (
    WITH original AS (SELECT lower(quiz_answer_fold(value)) AS name)
    SELECT COALESCE((
        SELECT string_agg(
            CASE WHEN token COLLATE "C" ~ '^[0-9]$'
                THEN (ARRAY['zero','one','two','three','four','five','six','seven','eight','nine'])[token::integer + 1]
                ELSE token
            END,
            ' ' ORDER BY position
        )
        FROM string_to_table(NULLIF(btrim(regexp_replace(name, '[[:punct:][:space:]]+', ' ', 'g')), ''), ' ')
             WITH ORDINALITY AS words(token, position)
    ), btrim(name)) -- Сохраняет названия из одних знаков, например «!!!».
    FROM original
);

ALTER TABLE artists ALTER COLUMN answer_search_name SET EXPRESSION AS (quiz_answer_search_key(name));
ALTER TABLE albums ALTER COLUMN answer_search_name SET EXPRESSION AS (quiz_answer_search_key(name));
ALTER TABLE tracks ALTER COLUMN answer_search_name SET EXPRESSION AS (quiz_answer_search_key(name));

ANALYZE artists (answer_search_name);
ANALYZE albums (answer_search_name);
ANALYZE tracks (answer_search_name);
