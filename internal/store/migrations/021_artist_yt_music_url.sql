ALTER TABLE artists ADD COLUMN yt_music_url text NOT NULL DEFAULT '';

CREATE OR REPLACE VIEW catalog_artists AS
SELECT a.id,a.name,a.full_name,a.country,a.logo_download_id,a.tags,a.artists,a.genius_id,
    a.music_brainz_id,a.manual_fields,a.error,a.lease_until,a.created_at,a.updated_at,
    a.yt_plays,a.yt_subs,a.asked,a.guessed,a.excluded_from_quiz,a.yt_music_id,a.members_known,m.missing_fields,
    CASE WHEN a.status IN ('parsed','partial') AND a.provider_parsed THEN
        CASE WHEN cardinality(m.missing_fields)>0 THEN 'partial' ELSE 'parsed' END
    WHEN a.status='parsed' AND cardinality(m.missing_fields)>0 THEN 'manual' ELSE a.status END AS status,
    COALESCE(NULLIF(a.yt_music_url,''), CASE WHEN a.yt_music_id IS NOT NULL
        THEN 'https://music.youtube.com/channel/' || a.yt_music_id ELSE '' END) AS yt_music_url
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
