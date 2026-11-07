CREATE TABLE tags (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE
);
ALTER TABLE albums ADD COLUMN tags text[] NOT NULL DEFAULT '{}';
ALTER TABLE tracks ADD COLUMN tags text[] NOT NULL DEFAULT '{}';

INSERT INTO tags(name)
SELECT DISTINCT tag FROM (
    SELECT unnest(tags) AS tag FROM artists
    UNION SELECT unnest(tags) AS tag FROM quiz_packs
) existing WHERE tag IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE FUNCTION register_entity_tags() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO tags(name) SELECT DISTINCT tag FROM unnest(NEW.tags) AS tag
    WHERE tag IS NOT NULL ON CONFLICT DO NOTHING;
    RETURN NEW;
END $$;

CREATE TRIGGER artists_register_tags AFTER INSERT OR UPDATE OF tags ON artists
    FOR EACH ROW EXECUTE FUNCTION register_entity_tags();
CREATE TRIGGER packs_register_tags AFTER INSERT OR UPDATE OF tags ON quiz_packs
    FOR EACH ROW EXECUTE FUNCTION register_entity_tags();
CREATE TRIGGER albums_register_tags AFTER INSERT OR UPDATE OF tags ON albums
    FOR EACH ROW EXECUTE FUNCTION register_entity_tags();
CREATE TRIGGER tracks_register_tags AFTER INSERT OR UPDATE OF tags ON tracks
    FOR EACH ROW EXECUTE FUNCTION register_entity_tags();

CREATE FUNCTION rename_entity_tags() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.name IS DISTINCT FROM NEW.name THEN
        UPDATE artists SET tags = array_replace(tags, OLD.name, NEW.name) WHERE OLD.name = ANY(tags);
        UPDATE quiz_packs SET tags = array_replace(tags, OLD.name, NEW.name) WHERE OLD.name = ANY(tags);
        UPDATE albums SET tags = array_replace(tags, OLD.name, NEW.name) WHERE OLD.name = ANY(tags);
        UPDATE tracks SET tags = array_replace(tags, OLD.name, NEW.name) WHERE OLD.name = ANY(tags);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER tags_rename AFTER UPDATE OF name ON tags FOR EACH ROW EXECUTE FUNCTION rename_entity_tags();

CREATE FUNCTION prevent_used_tag_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM artists WHERE OLD.name = ANY(tags)) OR
       EXISTS (SELECT 1 FROM quiz_packs WHERE OLD.name = ANY(tags)) OR
       EXISTS (SELECT 1 FROM albums WHERE OLD.name = ANY(tags)) OR
       EXISTS (SELECT 1 FROM tracks WHERE OLD.name = ANY(tags)) THEN
        RAISE EXCEPTION 'Тег используется' USING ERRCODE = '23503', CONSTRAINT = 'tag_in_use';
    END IF;
    RETURN OLD;
END $$;
CREATE TRIGGER tags_delete BEFORE DELETE ON tags FOR EACH ROW EXECUTE FUNCTION prevent_used_tag_delete();
