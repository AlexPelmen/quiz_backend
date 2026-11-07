-- Пометки в скобках скрыты из подсказки и не учитываются при выборе трека.
CREATE FUNCTION quiz_title_has_consonant(title text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
    cleaned text := title;
BEGIN
    WHILE cleaned ~ '\([^()]*\)' LOOP
        cleaned := regexp_replace(cleaned, '\([^()]*\)', '', 'g');
    END LOOP;

    RETURN cleaned ~ '[BCDFGHJKLMNPQRSTVWXZbcdfghjklmnpqrstvwxzБВГДЖЗЙКЛМНПРСТФХЦЧШЩбвгджзйклмнпрстфхцчшщ]';
END;
$$;
