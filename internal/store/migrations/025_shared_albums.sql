ALTER TABLE albums DROP CONSTRAINT album_owner;
ALTER TABLE artist_to_album DROP CONSTRAINT artist_to_album_pkey;
ALTER TABLE artist_to_album
    ADD PRIMARY KEY (album_id, artist_id),
    ADD COLUMN is_primary boolean NOT NULL DEFAULT true;
CREATE UNIQUE INDEX artist_to_album_one_primary ON artist_to_album (album_id) WHERE is_primary;

CREATE FUNCTION require_primary_album_artist() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE checked_id uuid;
BEGIN
    IF TG_TABLE_NAME = 'albums' THEN
        checked_id := NEW.id;
    ELSE
        checked_id := OLD.album_id;
    END IF;

    IF EXISTS (SELECT 1 FROM albums WHERE id = checked_id)
        AND NOT EXISTS (SELECT 1 FROM artist_to_album WHERE album_id = checked_id AND is_primary) THEN
        RAISE EXCEPTION 'Альбом должен иметь основного исполнителя'
            USING ERRCODE = '23503', CONSTRAINT = 'album_owner';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER album_requires_artist
    AFTER INSERT ON albums DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION require_primary_album_artist();

CREATE CONSTRAINT TRIGGER album_preserves_artist
    AFTER DELETE OR UPDATE OF album_id, is_primary ON artist_to_album DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (OLD.is_primary) EXECUTE FUNCTION require_primary_album_artist();

CREATE OR REPLACE VIEW catalog_albums AS
SELECT al.id,al.name,al.cover_download_id,al.release_date,al.tags,al.genius_id,al.track_list,
    al.track_list_complete,al.manual_release_date,al.created_at,al.updated_at,al.asked,al.guessed,
    al.excluded_from_quiz,al.yt_music_id,al.music_brainz_id,al.manual_fields,al.source_yt_plays,
    owner.artist_id,p.yt_plays,m.missing_fields,
    CASE WHEN EXISTS (SELECT 1 FROM album_parse_errors WHERE entity_id=al.id) AND cardinality(m.missing_fields)>0 THEN 'error'
         WHEN cardinality(m.missing_fields)=0 THEN 'parsed'
         WHEN a.provider_parsed THEN 'partial' ELSE 'manual' END AS status
FROM albums al JOIN artist_to_album owner ON owner.album_id=al.id AND owner.is_primary JOIN artists a ON a.id=owner.artist_id
LEFT JOIN downloads d ON d.id=al.cover_download_id
CROSS JOIN LATERAL (SELECT CASE WHEN 'yt_plays'=ANY(al.manual_fields) THEN al.yt_plays
    ELSE COALESCE(al.source_yt_plays,(SELECT max(t.yt_plays) FROM tracks t WHERE t.album_id=al.id))
    END AS yt_plays) p
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN al.release_date='' THEN 'release_date' END,
    CASE WHEN p.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'cover' END
],NULL) AS missing_fields) m;
