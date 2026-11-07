CREATE TABLE album_guessing (
    album_id uuid PRIMARY KEY REFERENCES albums(id) ON DELETE CASCADE,
    cover_download_id uuid NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
    approved boolean NOT NULL,
    rectangles jsonb NOT NULL CHECK (jsonb_typeof(rectangles) = 'array')
);

UPDATE quiz_questions SET image_download_id = NULL WHERE album_id IS NOT NULL;
ALTER TABLE quiz_questions ADD CONSTRAINT album_question_no_original
    CHECK (album_id IS NULL OR image_download_id IS NULL);
