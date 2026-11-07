CREATE TABLE downloads (
    id uuid PRIMARY KEY,
    source_type text NOT NULL CHECK (source_type IN ('youtube','mp3','image','unknown')),
    source_uri text NOT NULL DEFAULT '',
    uri text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('created','pending','processing','finished','errored','manual')),
    file_type text NOT NULL,
    meta jsonb NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX downloads_queue ON downloads(status, next_attempt_at, created_at);
CREATE UNIQUE INDEX downloads_source ON downloads(source_type, source_uri) WHERE source_uri <> '';

CREATE TABLE artists (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(trim(name)) > 0),
    full_name text NOT NULL DEFAULT '',
    country text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'created' CHECK (status IN ('created','processing','parsed','errored')),
    logo_download_id uuid REFERENCES downloads(id),
    tags text[] NOT NULL DEFAULT '{}',
    artists text[] NOT NULL DEFAULT '{}',
    points integer NOT NULL DEFAULT 100 CHECK (points BETWEEN 0 AND 100),
    genius_id bigint UNIQUE,
    music_brainz_id text NOT NULL DEFAULT '',
    manual_fields text[] NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX artists_name ON artists(lower(trim(name)));
CREATE INDEX artists_status ON artists(status);
CREATE INDEX artists_tags ON artists USING gin(tags);

CREATE TABLE albums (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(trim(name)) > 0),
    cover_download_id uuid REFERENCES downloads(id),
    release_date text NOT NULL DEFAULT '',
    artist_id uuid NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    points integer NOT NULL DEFAULT 100 CHECK (points BETWEEN 0 AND 100),
    genius_id bigint UNIQUE,
    track_list text NOT NULL DEFAULT '',
    track_list_complete boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX albums_artist ON albums(artist_id);

CREATE TABLE tracks (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(trim(name)) > 0),
    artist_id uuid NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    album_id uuid REFERENCES albums(id) ON DELETE SET NULL,
    points integer NOT NULL DEFAULT 100 CHECK (points BETWEEN 0 AND 100),
    genius_id bigint UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracks_album ON tracks(album_id);
CREATE INDEX tracks_artist ON tracks(artist_id);
CREATE TABLE track_to_download (
    track_id uuid NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    download_id uuid NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
    PRIMARY KEY (track_id, download_id)
);

CREATE TABLE settings (id boolean PRIMARY KEY DEFAULT true CHECK(id), value jsonb NOT NULL);
CREATE TABLE parse_runs (
    id uuid PRIMARY KEY,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','finished','errored')),
    processed integer NOT NULL DEFAULT 0,
    failed integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX one_active_run ON parse_runs((true)) WHERE status IN ('pending','running');

CREATE TABLE quiz_packs (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    tags text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE quiz_rounds (
    id uuid PRIMARY KEY,
    pack_id uuid NOT NULL REFERENCES quiz_packs(id) ON DELETE CASCADE,
    number integer NOT NULL CHECK(number BETWEEN 1 AND 10),
    type text NOT NULL,
    UNIQUE(pack_id, number)
);
CREATE TABLE quiz_questions (
    id uuid PRIMARY KEY,
    round_id uuid NOT NULL REFERENCES quiz_rounds(id) ON DELETE CASCADE,
    number integer NOT NULL CHECK(number BETWEEN 1 AND 100),
    artist_id uuid NOT NULL REFERENCES artists(id),
    track_id uuid NOT NULL REFERENCES tracks(id),
    primary_text text NOT NULL,
    secondary_text text NOT NULL DEFAULT '',
    image_download_id uuid REFERENCES downloads(id),
    sound_download_id uuid REFERENCES downloads(id),
    track_start_sec double precision,
    track_finish_sec double precision,
    answer text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((sound_download_id IS NULL AND track_start_sec IS NULL AND track_finish_sec IS NULL)
        OR (sound_download_id IS NOT NULL AND track_start_sec IS NOT NULL AND track_finish_sec IS NOT NULL
            AND track_start_sec >= 0
            AND track_finish_sec > track_start_sec AND track_finish_sec - track_start_sec <= 30)),
    UNIQUE(round_id, number)
);
