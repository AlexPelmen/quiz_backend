ALTER TABLE artist_album_shortlists DROP CONSTRAINT artist_album_shortlists_album_ids_check;
ALTER TABLE artist_album_shortlists ADD CHECK (cardinality(album_ids) BETWEEN 1 AND 5);

ALTER TABLE artist_provider_lists ADD COLUMN album_limit integer NOT NULL DEFAULT 5
    CHECK (album_limit BETWEEN 1 AND 5);
