CREATE OR REPLACE VIEW quiz_pack_status AS
SELECT p.id,p.name,p.description,p.tags,p.created_at,p.updated_at,p.difficulty,p.cover_download_id,
    CASE WHEN stats.incomplete=0 AND stats.total>0 THEN 'ready'
        WHEN stats.clip_error<>'' THEN 'new' ELSE p.status END AS status,
    stats.clips_total,stats.clips_ready,stats.clip_error,
    stats.total AS questions_total
FROM quiz_packs p
CROSS JOIN LATERAL (
    SELECT count(*) AS total,count(*) FILTER (WHERE q.status<>'ready') AS incomplete,
        count(*) FILTER (WHERE q.sound_download_id IS NOT NULL) AS clips_total,
        count(*) FILTER (WHERE q.sound_download_id IS NOT NULL AND q.status='ready') AS clips_ready,
        COALESCE(max(q.clip_error),'') AS clip_error
    FROM quiz_rounds r JOIN quiz_questions q ON q.round_id=r.id
    WHERE r.pack_id=p.id
) stats;
