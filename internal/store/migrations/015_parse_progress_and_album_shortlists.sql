CREATE TABLE parse_run_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES parse_runs(id) ON DELETE CASCADE,
    artist text NOT NULL DEFAULT '',
    stage text NOT NULL,
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX parse_run_events_recent ON parse_run_events(run_id, id DESC);

CREATE TABLE artist_album_shortlists (
    artist_id uuid PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    album_ids uuid[] NOT NULL CHECK (cardinality(album_ids) = 5)
);
