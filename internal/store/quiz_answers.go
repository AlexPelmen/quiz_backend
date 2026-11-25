package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// AnswerOptions ищет по названиям и подставляет исполнителя для найденных треков и альбомов.
func (r PackRepository) AnswerOptions(ctx context.Context, name, preferred string) ([]domain.AnswerOption, error) {
	return query[domain.AnswerOption](ctx, r.db, `WITH term AS (
		SELECT quiz_answer_search_key($1) AS key
	), patterns AS NOT MATERIALIZED (
		SELECT key,escaped || '%' AS prefix,translit,translit_escaped || '%' AS translit_prefix,
			CASE WHEN char_length(key)=1 OR key ~ '^[[:punct:][:space:]]+$' THEN escaped
				WHEN least(char_length(key),char_length($1))<=2 THEN escaped || '%'
				ELSE '%' || escaped || '%'
			END AS pattern,
			CASE WHEN char_length(translit)>=3 THEN '%' || translit_escaped || '%' END AS translit_pattern,
			CASE WHEN allow_typos AND first_half ~ '[[:alnum:]]{3}' THEN first_half || '%' END AS typo_prefix,
			CASE WHEN allow_typos AND second_half ~ '[[:alnum:]]{3}' THEN '%' || second_half || '%' END AS typo_tail
		FROM term
		CROSS JOIN LATERAL (
			SELECT regexp_count($1,'[[:alpha:]]')>5 AS allow_typos,
				left(key,char_length(key)/2) AS first_half,
				substr(key,char_length(key)/2+1) AS second_half
		) halves
		CROSS JOIN LATERAL (
			SELECT CASE WHEN least(char_length(key),char_length($1))>=3 AND key ~ '[[:alpha:]]'
				THEN quiz_answer_translit_key(key) END AS translit
		) t
		CROSS JOIN LATERAL (
			SELECT replace(replace(replace(key,'!','!!'),'%','!%'),'_','!_') AS escaped,
				replace(replace(replace(translit,'!','!!'),'%','!%'),'_','!_') AS translit_escaped
		) e
		WHERE key<>''
	), catalog AS NOT MATERIALIZED (
		SELECT id,name,yt_plays,answer_search_name,answer_translit_name,'artist'::text AS type FROM artists
		UNION ALL
		SELECT id,name,yt_plays,answer_search_name,answer_translit_name,'album' FROM albums
		UNION ALL
		SELECT id,name,yt_plays,answer_search_name,answer_translit_name,'track' FROM tracks
	), matches AS MATERIALIZED (
		SELECT id,name,type,yt_plays,
			CASE WHEN answer_search_name=key THEN 5
				WHEN answer_search_name LIKE prefix ESCAPE '!' THEN 4
				WHEN answer_search_name LIKE pattern ESCAPE '!' THEN 3
				WHEN answer_translit_name=translit THEN 2
				WHEN answer_translit_name LIKE translit_prefix ESCAPE '!' THEN 1
				ELSE 0
			END AS match_quality
		FROM catalog CROSS JOIN patterns
		WHERE answer_search_name LIKE pattern ESCAPE '!' OR answer_translit_name LIKE translit_pattern ESCAPE '!'
		ORDER BY match_quality DESC,(type=$2) DESC,yt_plays DESC NULLS LAST,name,id LIMIT 10
	), typo_matches AS (
		SELECT id,name,type,yt_plays,0 AS match_quality FROM catalog CROSS JOIN patterns
		WHERE NOT EXISTS (SELECT 1 FROM matches)
			-- При одной правке хотя бы одна половина запроса остаётся без изменений.
			AND (answer_search_name LIKE typo_prefix OR answer_search_name LIKE typo_tail)
			AND (
				quiz_answer_within_one_edit(key,left(answer_search_name,char_length(key)))
				OR quiz_answer_within_one_edit(key,left(answer_search_name,char_length(key)-1))
				OR quiz_answer_within_one_edit(key,left(answer_search_name,char_length(key)+1))
			)
		ORDER BY (type=$2) DESC,yt_plays DESC NULLS LAST,name,id LIMIT 10
	), top AS (
		SELECT * FROM matches UNION ALL SELECT * FROM typo_matches
	)
	SELECT top.id,top.name,top.type,COALESCE(a.name,'') AS artist
	FROM top
	LEFT JOIN artist_to_album aa ON top.type='album' AND aa.album_id=top.id AND aa.is_primary
	LEFT JOIN artist_to_track at ON top.type='track' AND at.track_id=top.id AND at.is_primary
	LEFT JOIN artists a ON a.id=COALESCE(aa.artist_id,at.artist_id)
	ORDER BY top.match_quality DESC,(top.type=$2) DESC,top.yt_plays DESC NULLS LAST,top.name,top.id`,
		[]any{pgx.QueryExecModeExec, name, preferred})
}

// PlayableQuestion содержит действующий вопрос и название выбранной записи каталога.
type PlayableQuestion struct {
	domain.Question
	RoundType        string
	OptionName       *string
	OptionNearAnswer bool
	OptionCoartist   bool
}

// PlayableQuestion возвращает вопрос готового пакета и проверяет выбранную запись в том же запросе.
func (r PackRepository) PlayableQuestion(
	ctx context.Context, id uuid.UUID, optionType string, optionID uuid.UUID,
) (PlayableQuestion, error) {
	return one[PlayableQuestion](ctx, r.db, `SELECT q.*,r.type AS round_type,option.name AS option_name,
		CASE WHEN regexp_count(q.answer,'[[:alpha:]]')>5 AND regexp_count(option.name,'[[:alpha:]]')>5
			THEN quiz_answer_within_one_edit(quiz_answer_search_key(q.answer),quiz_answer_search_key(option.name))
			ELSE false
		END AS option_near_answer,
		CASE WHEN $2<>'artist' THEN false
			WHEN q.track_id IS NOT NULL THEN EXISTS (
				SELECT 1 FROM artist_to_track owner WHERE owner.track_id=q.track_id AND owner.artist_id=$3
			)
			WHEN q.album_id IS NOT NULL THEN EXISTS (
				SELECT 1 FROM artist_to_album owner WHERE owner.album_id=q.album_id AND owner.artist_id=$3
			)
			WHEN r.type='artist_by_albums' THEN (
				SELECT count(owner.artist_id)>0
					AND count(owner.artist_id)=cardinality(string_to_array(q.secondary_text,E'\n'))
				FROM quiz_question_album_refs ar JOIN artist_to_album owner ON owner.album_id=ar.album_id
				WHERE ar.question_id=q.id AND owner.artist_id=$3
			)
			ELSE false
		END AS option_coartist
		FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id JOIN quiz_pack_status p ON p.id=r.pack_id
		LEFT JOIN (
			SELECT name FROM artists WHERE $2='artist' AND id=$3
			UNION ALL
			SELECT name FROM albums WHERE $2='album' AND id=$3
			UNION ALL
			SELECT name FROM tracks WHERE $2='track' AND id=$3
		) option ON true
		WHERE q.id=$1 AND NOT q.deleted AND p.status='ready' FOR SHARE OF q`, []any{id, optionType, optionID})
}

// SaveQuestionAnswer хранит первый отправленный ответ и возвращает его при повторах.
func (r PackRepository) SaveQuestionAnswer(
	ctx context.Context, keys domain.QuestionAttempt, answer domain.SubmittedAnswer,
) (domain.SubmittedAnswer, error) {
	result, err := r.db.Exec(ctx, `INSERT INTO quiz_question_answers(question_id,user_id,attempt_id,answer_text,correct)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		keys.QuestionID, keys.UserID, keys.AttemptID, answer.Text, answer.Correct)
	if err != nil || result.RowsAffected() > 0 {
		return answer, translate(err)
	}

	err = r.db.QueryRow(ctx, `SELECT answer_text,correct FROM quiz_question_answers
		WHERE question_id=$1 AND user_id=$2 AND attempt_id=$3`, keys.QuestionID, keys.UserID, keys.AttemptID).
		Scan(&answer.Text, &answer.Correct)

	return answer, translate(err)
}

// SubmittedRoundAnswers возвращает сохранённые отметки пользователя за раунд.
func (r PackRepository) SubmittedRoundAnswers(
	ctx context.Context, roundID, userID, attemptID uuid.UUID,
) (map[uuid.UUID]bool, error) {
	type row struct {
		QuestionID uuid.UUID
		Correct    bool
	}

	answers, err := query[row](ctx, r.db, `SELECT a.question_id,a.correct FROM quiz_questions q
		JOIN quiz_question_answers a ON a.question_id=q.id WHERE q.round_id=$1 AND a.user_id=$2 AND a.attempt_id=$3`,
		[]any{roundID, userID, attemptID})
	if err != nil {
		return nil, err
	}

	result := make(map[uuid.UUID]bool, len(answers))
	for _, answer := range answers {
		result[answer.QuestionID] = answer.Correct
	}

	return result, nil
}
