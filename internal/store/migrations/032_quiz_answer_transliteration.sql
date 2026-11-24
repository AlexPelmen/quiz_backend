-- Транслитерация русских букв совпадает с domain.TransliterateRussian.
CREATE FUNCTION quiz_answer_translit_key(value text) RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $$
DECLARE
    key text := translate(value, 'абвгдеёзийклмнопрстуфыэъь', 'abvgdeeziyklmnoprstufye');
    pair text[];
BEGIN
    FOREACH pair SLICE 1 IN ARRAY ARRAY[
        ['ж','zh'],['х','kh'],['ц','ts'],['ч','ch'],['ш','sh'],['щ','shch'],['ю','yu'],['я','ya']
    ] LOOP
        key := replace(key, pair[1], pair[2]);
    END LOOP;

    -- Arctic -> arktik, Rock -> rok, Placebo -> plasebo. Сочетание ch сохраняем для «ч».
    key := replace(key, 'ck', 'k');
    key := regexp_replace(key, 'c(?=[eiy])', 's', 'g');
    RETURN regexp_replace(key, 'c(?!h)', 'k', 'g');
END;
$$;

ALTER TABLE artists ADD COLUMN answer_translit_name text
    GENERATED ALWAYS AS (quiz_answer_translit_key(quiz_answer_search_key(name))) STORED;
ALTER TABLE albums ADD COLUMN answer_translit_name text
    GENERATED ALWAYS AS (quiz_answer_translit_key(quiz_answer_search_key(name))) STORED;
ALTER TABLE tracks ADD COLUMN answer_translit_name text
    GENERATED ALWAYS AS (quiz_answer_translit_key(quiz_answer_search_key(name))) STORED;

CREATE INDEX artists_answer_translit_trgm ON artists USING gin (answer_translit_name public.gin_trgm_ops);
CREATE INDEX albums_answer_translit_trgm ON albums USING gin (answer_translit_name public.gin_trgm_ops);
CREATE INDEX tracks_answer_translit_trgm ON tracks USING gin (answer_translit_name public.gin_trgm_ops);

ANALYZE artists (answer_search_name, answer_translit_name);
ANALYZE albums (answer_search_name, answer_translit_name);
ANALYZE tracks (answer_search_name, answer_translit_name);
