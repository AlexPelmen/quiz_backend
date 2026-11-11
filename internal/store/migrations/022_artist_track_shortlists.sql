CREATE TABLE artist_track_shortlists (
    artist_id uuid PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    track_ids uuid[] NOT NULL CHECK (cardinality(track_ids) BETWEEN 1 AND 8)
);

CREATE FUNCTION remove_deleted_track_from_shortlists() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM artist_provider_lists WHERE provider='yt_music' AND kind='tracks_ranked_20'
        AND artist_id IN (SELECT artist_id FROM artist_track_shortlists WHERE OLD.id=ANY(track_ids));
    DELETE FROM artist_track_shortlists WHERE OLD.id=ANY(track_ids) AND cardinality(track_ids)=1;
    UPDATE artist_track_shortlists SET track_ids=array_remove(track_ids, OLD.id) WHERE OLD.id=ANY(track_ids);
    RETURN OLD;
END;
$$;

CREATE TRIGGER track_shortlists_remove_deleted BEFORE DELETE ON tracks
    FOR EACH ROW EXECUTE FUNCTION remove_deleted_track_from_shortlists();
