CREATE FUNCTION quiz_popularity_weight(points double precision, center double precision)
RETURNS double precision LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT CASE WHEN center = 0 THEN points ELSE exp(-0.5 * power((points - center) / 15, 2)) END
$$;
