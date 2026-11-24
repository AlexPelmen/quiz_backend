-- Проверяет, не превышает ли расстояние Левенштейна единицу.
CREATE FUNCTION quiz_answer_within_one_edit(value text, expected text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $$
DECLARE
    shorter text := value;
    longer text := expected;
    shorter_length int := char_length(value);
    longer_length int := char_length(expected);
    first_difference int := 1;
BEGIN
    IF shorter_length > longer_length THEN
        shorter := expected;
        longer := value;
        shorter_length := char_length(expected);
        longer_length := char_length(value);
    END IF;

    IF longer_length - shorter_length > 1 THEN
        RETURN false;
    END IF;

    WHILE first_difference <= shorter_length
        AND substr(shorter, first_difference, 1) = substr(longer, first_difference, 1)
    LOOP
        first_difference := first_difference + 1;
    END LOOP;

    -- При замене пропускаем по букве в обеих строках, при вставке только в длинной.
    RETURN substr(shorter, first_difference + (shorter_length = longer_length)::int)
        = substr(longer, first_difference + 1);
END;
$$;
