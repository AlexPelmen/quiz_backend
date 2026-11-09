-- Сбор студийных альбомов больше не требует треклист для готовности записи.
CREATE OR REPLACE VIEW catalog_albums AS
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
    CASE WHEN p.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN d.id IS NULL OR d.status IN ('manual','errored') THEN 'cover' END
],NULL) AS missing_fields) m;
