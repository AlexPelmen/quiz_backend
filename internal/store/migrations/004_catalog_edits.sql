ALTER TABLE albums ADD COLUMN manual_release_date boolean NOT NULL DEFAULT false;

ALTER TABLE downloads DROP CONSTRAINT downloads_source_type_check;
ALTER TABLE downloads ADD CONSTRAINT downloads_source_type_check
    CHECK (source_type IN ('youtube','mp3','image','unknown','upload'));
