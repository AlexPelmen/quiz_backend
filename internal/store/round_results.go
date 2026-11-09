package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// RoundTransaction видит уже записанный результат при одновременной повторной отправке.
func (u *UnitOfWork) RoundTransaction(ctx context.Context, fn func(*UnitOfWork) error) error {
	return u.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// RoundQuestions блокирует удаление раунда до записи результата.
func (r PackRepository) RoundQuestions(ctx context.Context, id uuid.UUID) ([]domain.Question, error) {
	var roundID uuid.UUID

	err := r.db.QueryRow(ctx, "SELECT id FROM quiz_rounds WHERE id=$1 FOR KEY SHARE", id).Scan(&roundID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.ErrNotFound
		}

		return nil, translate(err)
	}

	return query[domain.Question](ctx, r.db,
		"SELECT * FROM quiz_questions WHERE round_id=$1 ORDER BY number", []any{roundID})
}

// RoundResult хранит проверенные ответы в порядке вопросов раунда.
type RoundResult struct {
	RoundID   uuid.UUID
	AttemptID uuid.UUID
	Correct   int
	Answers   []bool
}

// SaveRoundResult возвращает true только при первой записи этой попытки.
func (r PackRepository) SaveRoundResult(ctx context.Context, result RoundResult) (bool, error) {
	tag, err := r.db.Exec(ctx, `INSERT INTO quiz_round_results(round_id,attempt_id,r,n,answers)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		result.RoundID, result.AttemptID, result.Correct, len(result.Answers), result.Answers)
	if err != nil {
		return false, translate(err)
	}

	if tag.RowsAffected() > 0 {
		return true, nil
	}

	var saved []bool
	if err := r.db.QueryRow(ctx, "SELECT answers FROM quiz_round_results WHERE round_id=$1 AND attempt_id=$2",
		result.RoundID, result.AttemptID).Scan(&saved); err != nil {
		return false, translate(err)
	}

	if !slices.Equal(result.Answers, saved) {
		return false, fmt.Errorf("%w: попытка уже сохранена с другими ответами", domain.ErrConflict)
	}

	return false, nil
}

// CountAnswer засчитывает вопрос исполнителю и при наличии треку или альбому.
func (r PackRepository) CountAnswer(ctx context.Context, q domain.Question, correct bool) error {
	const increment = " SET asked=asked+1,guessed=guessed+$2::integer,updated_at=now() WHERE id=$1"

	var guessed int
	if correct {
		guessed = 1
	}

	if err := execute(ctx, r.db, "UPDATE artists"+increment, []any{q.ArtistID, guessed}); err != nil {
		return err
	}

	if q.TrackID != nil {
		return execute(ctx, r.db, "UPDATE tracks"+increment, []any{*q.TrackID, guessed})
	}

	if q.AlbumID != nil {
		return execute(ctx, r.db, "UPDATE albums"+increment, []any{*q.AlbumID, guessed})
	}

	return nil
}
