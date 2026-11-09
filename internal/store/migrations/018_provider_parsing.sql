ALTER TABLE parse_runs
    ADD COLUMN provider text NOT NULL DEFAULT '' CHECK (provider IN ('','yt_music','genius','musicbrainz')),
    ADD COLUMN fields integer NOT NULL DEFAULT 0,
    ADD COLUMN albums integer NOT NULL DEFAULT 0,
    ADD COLUMN tracks integer NOT NULL DEFAULT 0,
    ADD COLUMN errors integer NOT NULL DEFAULT 0;

ALTER TABLE artists DROP CONSTRAINT artists_status_check;
ALTER TABLE artists ADD CONSTRAINT artists_status_check
    CHECK (status IN ('created','processing','parsed','partial','error','errored','manual'));
ALTER TABLE artists ADD COLUMN provider_parsed boolean NOT NULL DEFAULT false;

CREATE TABLE artist_provider_lists (
    artist_id uuid NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    provider text NOT NULL,
    kind text NOT NULL,
    PRIMARY KEY (artist_id, provider, kind)
);

CREATE OR REPLACE VIEW catalog_artists AS
SELECT a.id,a.name,a.full_name,a.country,a.logo_download_id,a.tags,a.artists,a.genius_id,
    a.music_brainz_id,a.manual_fields,a.error,a.lease_until,a.created_at,a.updated_at,
    a.yt_plays,a.yt_subs,a.asked,a.guessed,a.excluded_from_quiz,a.yt_music_id,a.members_known,m.missing_fields,
    CASE WHEN a.status IN ('parsed','partial') AND a.provider_parsed THEN
        CASE WHEN cardinality(m.missing_fields)>0 THEN 'partial' ELSE 'parsed' END
    WHEN a.status='parsed' AND cardinality(m.missing_fields)>0 THEN 'manual' ELSE a.status END AS status
FROM artists a LEFT JOIN downloads d ON d.id=a.logo_download_id
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN a.full_name='' THEN 'full_name' END,
    CASE WHEN a.country='' THEN 'country' END,
    CASE WHEN NOT a.members_known THEN 'artists' END,
    CASE WHEN a.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN a.yt_subs IS NULL THEN 'yt_subs' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'logo' END,
    CASE WHEN a.provider_parsed AND NOT EXISTS (SELECT 1 FROM artist_to_album WHERE artist_id=a.id) THEN 'albums' END,
    CASE WHEN a.provider_parsed AND NOT EXISTS (SELECT 1 FROM artist_to_track WHERE artist_id=a.id) THEN 'tracks' END
],NULL) AS missing_fields) m;

CREATE TABLE album_parse_errors (
    entity_id uuid NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    provider text NOT NULL,
    scenario text NOT NULL,
    PRIMARY KEY (entity_id, provider, scenario)
);
CREATE TABLE track_parse_errors (
    entity_id uuid NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    provider text NOT NULL,
    scenario text NOT NULL,
    PRIMARY KEY (entity_id, provider, scenario)
);

CREATE OR REPLACE VIEW catalog_albums AS
SELECT al.id,al.name,al.cover_download_id,al.release_date,al.tags,al.genius_id,al.track_list,
    al.track_list_complete,al.manual_release_date,al.created_at,al.updated_at,al.asked,al.guessed,
    al.excluded_from_quiz,al.yt_music_id,al.music_brainz_id,al.manual_fields,al.source_yt_plays,
    owner.artist_id,p.yt_plays,m.missing_fields,
    CASE WHEN EXISTS (SELECT 1 FROM album_parse_errors WHERE entity_id=al.id) AND cardinality(m.missing_fields)>0 THEN 'error'
         WHEN cardinality(m.missing_fields)=0 THEN 'parsed'
         WHEN a.provider_parsed THEN 'partial' ELSE 'manual' END AS status
FROM albums al JOIN artist_to_album owner ON owner.album_id=al.id JOIN artists a ON a.id=owner.artist_id
LEFT JOIN downloads d ON d.id=al.cover_download_id
CROSS JOIN LATERAL (SELECT CASE WHEN 'yt_plays'=ANY(al.manual_fields) THEN al.yt_plays
    ELSE COALESCE(al.source_yt_plays,(SELECT max(t.yt_plays) FROM tracks t WHERE t.album_id=al.id))
    END AS yt_plays) p
CROSS JOIN LATERAL (SELECT array_remove(ARRAY[
    CASE WHEN al.release_date='' THEN 'release_date' END,
    CASE WHEN p.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'cover' END
],NULL) AS missing_fields) m;

CREATE OR REPLACE VIEW catalog_tracks AS
SELECT t.*,owner.artist_id,m.missing_fields,
    CASE WHEN EXISTS (SELECT 1 FROM track_parse_errors WHERE entity_id=t.id) AND cardinality(m.missing_fields)>0 THEN 'error'
         WHEN cardinality(m.missing_fields)=0 THEN 'parsed'
         WHEN a.provider_parsed THEN 'partial' ELSE 'manual' END AS status,
    COALESCE(audio.status,'missing') AS audio_status
FROM tracks t JOIN artist_to_track owner ON owner.track_id=t.id JOIN artists a ON a.id=owner.artist_id
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
