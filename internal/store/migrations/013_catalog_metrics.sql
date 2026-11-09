CREATE TABLE legacy_catalog_popularity (
    entity text NOT NULL,
    id uuid NOT NULL,
    points double precision NOT NULL,
    guess_num integer NOT NULL,
    PRIMARY KEY (entity, id)
);

DO $$
DECLARE entity text;
BEGIN
    FOREACH entity IN ARRAY ARRAY['artists','albums','tracks'] LOOP
        EXECUTE format('INSERT INTO legacy_catalog_popularity SELECT %L,id,points,guess_num FROM %I', entity, entity);
        EXECUTE format('ALTER TABLE %I
            ADD COLUMN yt_plays bigint CHECK (yt_plays >= 0),
            ADD COLUMN asked bigint NOT NULL DEFAULT 0 CHECK (asked >= 0),
            ADD COLUMN guessed bigint NOT NULL DEFAULT 0 CHECK (guessed BETWEEN 0 AND asked),
            ADD COLUMN excluded_from_quiz boolean NOT NULL DEFAULT false,
            ADD COLUMN yt_music_id text UNIQUE,
            ADD COLUMN IF NOT EXISTS music_brainz_id text NOT NULL DEFAULT %L', entity, '');
        EXECUTE format('UPDATE %I SET excluded_from_quiz = (points = 0),
            asked = CASE WHEN points <> trunc(points) THEN 3 ELSE 0 END,
            guessed = CASE WHEN points <> trunc(points) THEN round(points * 3 / 100)::bigint ELSE 0 END', entity);
        EXECUTE format('ALTER TABLE %I DROP COLUMN points, DROP COLUMN guess_num', entity);
    END LOOP;
END $$;

ALTER TABLE artists ADD COLUMN yt_subs bigint CHECK (yt_subs >= 0),
    ADD COLUMN members_known boolean NOT NULL DEFAULT false;
UPDATE artists SET members_known = cardinality(artists) > 0 OR 'artists' = ANY(manual_fields);
ALTER TABLE albums ADD COLUMN manual_fields text[] NOT NULL DEFAULT '{}',
    ADD COLUMN source_yt_plays bigint CHECK (source_yt_plays >= 0);
ALTER TABLE tracks ADD COLUMN manual_fields text[] NOT NULL DEFAULT '{}',
    ADD COLUMN duration_seconds integer CHECK (duration_seconds > 0);
CREATE UNIQUE INDEX albums_music_brainz ON albums(music_brainz_id) WHERE music_brainz_id <> '';
CREATE UNIQUE INDEX tracks_music_brainz ON tracks(music_brainz_id) WHERE music_brainz_id <> '';

CREATE TABLE artist_to_track (
    track_id uuid PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    artist_id uuid NOT NULL REFERENCES artists(id)
);
CREATE TABLE artist_to_album (
    album_id uuid PRIMARY KEY REFERENCES albums(id) ON DELETE CASCADE,
    artist_id uuid NOT NULL REFERENCES artists(id)
);
CREATE INDEX artist_to_track_artist ON artist_to_track(artist_id);
CREATE INDEX artist_to_album_artist ON artist_to_album(artist_id);
INSERT INTO artist_to_track SELECT id, artist_id FROM tracks;
INSERT INTO artist_to_album SELECT id, artist_id FROM albums;
-- Обратные отложенные FK проверяют полноту переноса и требуют владельца при записи.
ALTER TABLE tracks ADD CONSTRAINT track_owner FOREIGN KEY (id) REFERENCES artist_to_track(track_id)
    DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE albums ADD CONSTRAINT album_owner FOREIGN KEY (id) REFERENCES artist_to_album(album_id)
    DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE tracks DROP COLUMN artist_id;
ALTER TABLE albums DROP COLUMN artist_id;

ALTER TABLE downloads ADD COLUMN downloader text NOT NULL DEFAULT ''
    CHECK (downloader IN ('','yt_music','hitmo','genius_youtube','genius_short','image','direct')),
    ADD COLUMN preview boolean NOT NULL DEFAULT false;
UPDATE downloads SET status='created', lease_until=NULL WHERE status IN ('pending','processing');
ALTER TABLE downloads ALTER COLUMN status SET DEFAULT 'created';

UPDATE settings SET value = (value - ARRAY['k_parse','k_popularity','min_popularity_delta','source'])
    || '{"quiz_guess_rate_enabled":true,"quiz_guess_rate_asked_threshold":5}'::jsonb;
DROP FUNCTION quiz_popularity_weight(double precision, double precision);

CREATE VIEW catalog_artists AS
SELECT a.id,a.name,a.full_name,a.country,a.logo_download_id,a.tags,a.artists,a.genius_id,
    a.music_brainz_id,a.manual_fields,a.error,a.lease_until,a.created_at,a.updated_at,
    a.yt_plays,a.yt_subs,a.asked,a.guessed,a.excluded_from_quiz,a.yt_music_id,a.members_known,m.missing_fields,
    CASE WHEN a.status='parsed' AND cardinality(m.missing_fields)>0 THEN 'manual' ELSE a.status END AS status
FROM artists a LEFT JOIN downloads d ON d.id=a.logo_download_id
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN a.full_name='' THEN 'full_name' END,
    CASE WHEN a.country='' THEN 'country' END,
    CASE WHEN NOT a.members_known THEN 'artists' END,
    CASE WHEN a.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN a.yt_subs IS NULL THEN 'yt_subs' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'logo' END
],NULL) AS missing_fields) m;

CREATE VIEW catalog_albums AS
SELECT al.id,al.name,al.cover_download_id,al.release_date,al.tags,al.genius_id,al.track_list,
    al.track_list_complete,al.manual_release_date,al.created_at,al.updated_at,al.asked,al.guessed,
    al.excluded_from_quiz,al.yt_music_id,al.music_brainz_id,al.manual_fields,al.source_yt_plays,
    owner.artist_id,p.yt_plays,m.missing_fields,
    CASE WHEN cardinality(m.missing_fields)>0 THEN 'manual' ELSE 'parsed' END AS status
FROM albums al JOIN artist_to_album owner ON owner.album_id=al.id
LEFT JOIN downloads d ON d.id=al.cover_download_id
CROSS JOIN LATERAL (SELECT CASE WHEN 'yt_plays'=ANY(al.manual_fields) THEN al.yt_plays
    ELSE GREATEST(al.source_yt_plays,(SELECT max(t.yt_plays) FROM tracks t WHERE t.album_id=al.id))
    END AS yt_plays) p
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN al.release_date='' THEN 'release_date' END,
    CASE WHEN NOT al.track_list_complete THEN 'track_list' END,
    CASE WHEN p.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'cover' END
],NULL) AS missing_fields) m;

CREATE VIEW catalog_tracks AS
SELECT t.*,owner.artist_id,m.missing_fields,
    CASE WHEN cardinality(m.missing_fields)>0 THEN 'manual' ELSE 'parsed' END AS status,
    COALESCE(audio.status,'missing') AS audio_status
FROM tracks t JOIN artist_to_track owner ON owner.track_id=t.id
LEFT JOIN LATERAL (
    SELECT d.status FROM track_to_download td JOIN downloads d ON d.id=td.download_id
    WHERE td.track_id=t.id AND d.file_type='mp3'
    ORDER BY CASE d.status WHEN 'finished' THEN 0 WHEN 'processing' THEN 1
        WHEN 'pending' THEN 2 WHEN 'created' THEN 3 WHEN 'errored' THEN 4 ELSE 5 END
    LIMIT 1
) audio ON true
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN t.album_id IS NULL THEN 'album_id' END,
    CASE WHEN t.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN t.duration_seconds IS NULL THEN 'duration_seconds' END,
    CASE WHEN audio.status IS NULL OR audio.status IN ('manual','errored') THEN 'audio' END
],NULL) AS missing_fields) m;
