-- Отсутствие альбома не делает трек неполным: для квиза достаточно его аудио.
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
    CASE WHEN t.yt_plays IS NULL THEN 'yt_plays' END,
    CASE WHEN t.duration_seconds IS NULL THEN 'duration_seconds' END,
    CASE WHEN audio.status IS NULL OR audio.status IN ('manual','errored') THEN 'audio' END
],NULL) AS missing_fields) m;
