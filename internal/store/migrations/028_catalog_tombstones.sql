-- Удалённые записи оставляют только идентификатор, тип и время удаления.
CREATE TABLE deleted_catalog_entries (
    resource text NOT NULL CHECK (resource IN ('artists','tracks','albums')),
    id uuid NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource,id)
);

CREATE FUNCTION archive_catalog_id() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_catalog_entries(resource,id) VALUES (TG_TABLE_NAME,OLD.id);
    DELETE FROM legacy_catalog_popularity WHERE entity=TG_TABLE_NAME AND id=OLD.id;
    RETURN NULL;
END;
$$;

CREATE TRIGGER archive_artist AFTER DELETE ON artists FOR EACH ROW EXECUTE FUNCTION archive_catalog_id();
CREATE TRIGGER archive_track AFTER DELETE ON tracks FOR EACH ROW EXECUTE FUNCTION archive_catalog_id();
CREATE TRIGGER archive_album AFTER DELETE ON albums FOR EACH ROW EXECUTE FUNCTION archive_catalog_id();

ALTER TABLE quiz_questions
    ADD COLUMN deleted boolean NOT NULL DEFAULT false,
    ADD COLUMN clip_error text NOT NULL DEFAULT '',
    DROP CONSTRAINT quiz_questions_artist_id_fkey,
    DROP CONSTRAINT quiz_questions_track_id_fkey,
    DROP CONSTRAINT quiz_questions_album_id_fkey,
    ALTER COLUMN artist_id DROP NOT NULL,
    ADD FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE SET NULL,
    ADD FOREIGN KEY (track_id) REFERENCES tracks(id) ON DELETE SET NULL,
    ADD FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE SET NULL,
    ADD CHECK (deleted = (artist_id IS NULL));

CREATE INDEX quiz_questions_artist_ref ON quiz_questions(artist_id) WHERE artist_id IS NOT NULL;
CREATE INDEX quiz_questions_track_ref ON quiz_questions(track_id) WHERE track_id IS NOT NULL;
CREATE INDEX quiz_questions_album_ref ON quiz_questions(album_id) WHERE album_id IS NOT NULL;

CREATE FUNCTION scrub_deleted_question() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.deleted := true;
    NEW.artist_id := NULL;
    NEW.track_id := NULL;
    NEW.album_id := NULL;
    NEW.primary_text := 'Вопрос был удалён';
    NEW.secondary_text := '';
    NEW.answer := '';
    NEW.answer_info := '{}';
    NEW.image_download_id := NULL;
    NEW.sound_download_id := NULL;
    NEW.track_start_sec := NULL;
    NEW.track_finish_sec := NULL;
    NEW.status := 'ready';
    NEW.clip_name := '';
    NEW.clip_error := '';
    NEW.clip_lease_until := NULL;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER scrub_deleted_question BEFORE UPDATE OF deleted,artist_id,track_id,album_id ON quiz_questions
    FOR EACH ROW WHEN (NOT OLD.deleted AND (NEW.deleted
        OR (OLD.artist_id IS NOT NULL AND NEW.artist_id IS NULL)
        OR (OLD.track_id IS NOT NULL AND NEW.track_id IS NULL)
        OR (OLD.album_id IS NOT NULL AND NEW.album_id IS NULL)))
    EXECUTE FUNCTION scrub_deleted_question();

-- Дополнительные ссылки нужны для альбомов из подсказок и метаданных аудиовопросов.
CREATE TABLE quiz_question_album_refs (
    question_id uuid NOT NULL REFERENCES quiz_questions(id) ON DELETE CASCADE,
    album_id uuid NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id,album_id)
);
CREATE INDEX quiz_question_album_refs_album ON quiz_question_album_refs(album_id);

-- В старых пакетах эти альбомы записаны только по названиям.
CREATE FUNCTION catalog_question_album_name(value text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE cleaned text;
BEGIN
    LOOP
        cleaned := regexp_replace(value, '\([^()]*\)', '', 'g');
        EXIT WHEN cleaned = value;
        value := cleaned;
    END LOOP;
    RETURN trim(regexp_replace(cleaned, '[[:space:]]+', ' ', 'g'));
END;
$$;

INSERT INTO quiz_question_album_refs(question_id,album_id)
SELECT q.id,al.id FROM quiz_questions q JOIN tracks t ON t.id=q.track_id JOIN albums al ON al.id=t.album_id
WHERE catalog_question_album_name(al.name)=q.answer_info->>'album'
UNION
SELECT q.id,al.id FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id AND r.type='artist_by_albums'
JOIN artist_to_album owner ON owner.artist_id=q.artist_id AND owner.is_primary
JOIN albums al ON al.id=owner.album_id
WHERE catalog_question_album_name(al.name)=ANY(string_to_array(q.secondary_text,E'\n'));

DROP FUNCTION catalog_question_album_name(text);

CREATE FUNCTION retire_question_album_ref() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE quiz_questions SET deleted=true WHERE id=OLD.question_id AND NOT deleted;
    RETURN NULL;
END;
$$;

CREATE TRIGGER retire_question_album_ref AFTER DELETE ON quiz_question_album_refs
    FOR EACH ROW EXECUTE FUNCTION retire_question_album_ref();

-- Ошибка относится к первому незавершённому клипу. При удалении вопроса она очищается вместе с ним.
UPDATE quiz_questions q SET clip_error=p.clip_error
FROM quiz_packs p
WHERE p.clip_error<>'' AND q.id=(
    SELECT pending.id FROM quiz_questions pending JOIN quiz_rounds r ON r.id=pending.round_id
    WHERE r.pack_id=p.id AND pending.status<>'ready' ORDER BY pending.number LIMIT 1
);
ALTER TABLE quiz_packs DROP COLUMN clip_error;

CREATE VIEW quiz_pack_status AS
SELECT p.id,p.name,p.description,p.tags,p.created_at,p.updated_at,p.difficulty,p.cover_download_id,
    CASE WHEN stats.incomplete=0 AND stats.total>0 THEN 'ready'
        WHEN stats.clip_error<>'' THEN 'new' ELSE p.status END AS status,
    stats.clips_total,stats.clips_ready,stats.clip_error
FROM quiz_packs p
CROSS JOIN LATERAL (
    SELECT count(*) AS total,count(*) FILTER (WHERE q.status<>'ready') AS incomplete,
        count(*) FILTER (WHERE q.sound_download_id IS NOT NULL) AS clips_total,
        count(*) FILTER (WHERE q.sound_download_id IS NOT NULL AND q.status='ready') AS clips_ready,
        COALESCE(max(q.clip_error),'') AS clip_error
    FROM quiz_rounds r JOIN quiz_questions q ON q.round_id=r.id
    WHERE r.pack_id=p.id
) stats;
