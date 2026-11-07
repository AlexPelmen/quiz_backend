ALTER TABLE quiz_packs
    ADD COLUMN difficulty text NOT NULL DEFAULT 'medium' CHECK (difficulty IN ('easy','medium','hard')),
    ADD COLUMN cover_download_id uuid REFERENCES downloads(id) ON DELETE SET NULL;
