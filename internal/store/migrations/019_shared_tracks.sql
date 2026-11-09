ALTER TABLE tracks DROP CONSTRAINT track_owner;
ALTER TABLE artist_to_track DROP CONSTRAINT artist_to_track_pkey;
ALTER TABLE artist_to_track
    ADD PRIMARY KEY (track_id, artist_id),
    ADD COLUMN is_primary boolean NOT NULL DEFAULT true;
CREATE UNIQUE INDEX artist_to_track_one_primary ON artist_to_track (track_id) WHERE is_primary;

CREATE FUNCTION require_primary_track_artist() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE checked_id uuid;
BEGIN
    IF TG_TABLE_NAME = 'tracks' THEN
        checked_id := NEW.id;
    ELSE
        checked_id := OLD.track_id;
    END IF;

    IF EXISTS (SELECT 1 FROM tracks WHERE id = checked_id)
        AND NOT EXISTS (SELECT 1 FROM artist_to_track WHERE track_id = checked_id AND is_primary) THEN
        RAISE EXCEPTION 'Трек должен иметь основного исполнителя'
            USING ERRCODE = '23503', CONSTRAINT = 'track_owner';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER track_requires_artist
    AFTER INSERT ON tracks DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_primary_track_artist();

CREATE CONSTRAINT TRIGGER track_preserves_artist
    AFTER DELETE OR UPDATE OF track_id, is_primary ON artist_to_track DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (OLD.is_primary) EXECUTE FUNCTION require_primary_track_artist();
