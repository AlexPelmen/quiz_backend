CREATE VIEW question_feedback_context AS
SELECT q.id AS question_id, p.name AS pack_name, r.type AS round_type,
       q.number, q.deleted, q.primary_text, q.secondary_text, q.answer, q.artist_id, q.track_id,
       COALESCE(NULLIF(q.answer_info->>'artist',''),a.name,'') AS artist_name,
       COALESCE(NULLIF(q.answer_info->>'track',''),t.name,'') AS track_name,
       COALESCE(NULLIF(q.answer_info->>'album',''),al.name,'') AS album_name,
       COALESCE(NULLIF(a.yt_music_url,''),'https://music.youtube.com/channel/' || a.yt_music_id,'') AS yt_music_url,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id',album.id,'name',album.name) ORDER BY album.name,album.id)
           FROM albums album WHERE album.id=q.album_id OR EXISTS (
               SELECT 1 FROM quiz_question_album_refs ar WHERE ar.question_id=q.id AND ar.album_id=album.id
           )),'[]'::jsonb) AS albums,
       COALESCE((SELECT jsonb_agg(other.name ORDER BY owner.is_primary DESC,other.name,other.id)
           FROM (
               SELECT artist_id,is_primary FROM artist_to_track WHERE track_id=q.track_id
               UNION ALL
               SELECT artist_id,is_primary FROM artist_to_album WHERE album_id=q.album_id
           ) owner JOIN artists other ON other.id=owner.artist_id
           WHERE owner.artist_id<>q.artist_id),'[]'::jsonb) AS coartists,
       COALESCE(q.image_download_id,cover.id) AS image_download_id,
       COALESCE(g.rectangles,'[]'::jsonb) AS cover_rectangles,
       COALESCE(q.image_download_id,logo.id,CASE WHEN r.type='track_vowels' THEN album_cover.id END) AS answer_image_download_id,
       COALESCE(CASE WHEN r.type='track_vowels' THEN al.release_date END,'') AS release_date,
       q.track_start_sec, q.track_finish_sec, q.clip_name,
       q.clip_name<>'' AS has_audio, (q.image_download_id IS NOT NULL OR cover.id IS NOT NULL) AS has_image,
       (q.image_download_id IS DISTINCT FROM COALESCE(q.image_download_id,logo.id,
           CASE WHEN r.type='track_vowels' THEN album_cover.id END)) AS has_answer_image
FROM quiz_questions q
JOIN quiz_rounds r ON r.id=q.round_id
JOIN quiz_packs p ON p.id=r.pack_id
LEFT JOIN artists a ON a.id=q.artist_id
LEFT JOIN tracks t ON t.id=q.track_id
LEFT JOIN albums al ON al.id=COALESCE(q.album_id,t.album_id)
LEFT JOIN album_guessing g ON r.type='artist_by_cover' AND g.album_id=al.id
    AND g.cover_download_id=al.cover_download_id AND g.approved
LEFT JOIN downloads cover ON cover.id=g.cover_download_id AND cover.status='finished'
LEFT JOIN downloads logo ON logo.id=a.logo_download_id AND logo.status='finished'
LEFT JOIN downloads album_cover ON album_cover.id=al.cover_download_id AND album_cover.status='finished';

ALTER TABLE question_feedback ADD COLUMN snapshot jsonb;
UPDATE question_feedback f SET snapshot=to_jsonb(q)
FROM question_feedback_context q WHERE q.question_id=f.question_id AND f.type<>'like';

ALTER TABLE question_feedback ADD CHECK ((type='like') = (snapshot IS NULL));
DROP INDEX question_feedback_unread;
ALTER TABLE question_feedback DROP COLUMN viewed;
CREATE INDEX question_feedback_reports ON question_feedback(created_at DESC,id DESC) WHERE type<>'like';
