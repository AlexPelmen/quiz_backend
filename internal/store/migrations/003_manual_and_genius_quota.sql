ALTER TABLE artists DROP CONSTRAINT artists_status_check;
ALTER TABLE artists ADD CONSTRAINT artists_status_check
    CHECK (status IN ('created','processing','parsed','errored','manual'));

CREATE TABLE genius_quota (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    quota_limit bigint NOT NULL,
    remaining bigint NOT NULL,
    reset_at timestamptz NOT NULL,
    observed_at timestamptz NOT NULL
);
