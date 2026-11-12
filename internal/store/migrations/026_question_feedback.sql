CREATE TABLE question_feedback (
 id uuid PRIMARY KEY,
 question_id uuid NOT NULL REFERENCES quiz_questions(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id),
 type text NOT NULL CHECK (type IN ('question_error','bad_audio','bug','like')),
 comment text NOT NULL DEFAULT '',
 viewed boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((type='like' AND comment='') OR (type<>'like' AND length(trim(comment)) BETWEEN 1 AND 4000))
);
CREATE UNIQUE INDEX question_feedback_one_like ON question_feedback(question_id,user_id) WHERE type='like';
CREATE INDEX question_feedback_question ON question_feedback(question_id,created_at DESC);
CREATE INDEX question_feedback_unread ON question_feedback(question_id) WHERE type<>'like' AND NOT viewed;
