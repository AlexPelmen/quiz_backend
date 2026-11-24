ALTER TABLE quiz_question_answers DROP CONSTRAINT quiz_question_answers_answer_text_check;
ALTER TABLE quiz_question_answers ADD CONSTRAINT quiz_question_answers_correct_requires_text
    CHECK (NOT correct OR answer_text <> '');
