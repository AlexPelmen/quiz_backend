package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// QuizClipTask связывает вопрос и пакет, для которого готовится отрывок.
type QuizClipTask struct {
	PackID   uuid.UUID
	Question domain.Question
}

const incompleteQuestions = `FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id
	WHERE r.pack_id=p.id AND q.status<>'ready'`

// ClaimQuizClip резервирует один вопрос. В одном пакете одновременно готовится один клип.
func (u *UnitOfWork) ClaimQuizClip(ctx context.Context, seconds int) (QuizClipTask, error) {
	var task QuizClipTask

	err := u.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx *UnitOfWork) error {
		pack, err := one[struct{ ID uuid.UUID }](ctx, tx.db, `SELECT p.id FROM quiz_packs p
			WHERE p.status<>'ready'
			AND EXISTS (SELECT 1 `+incompleteQuestions+`)
			AND NOT EXISTS (SELECT 1 `+incompleteQuestions+` AND (q.clip_lease_until>now() OR q.clip_error<>''))
			ORDER BY p.created_at,p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED`, nil)
		if err != nil {
			return err
		}

		task.PackID = pack.ID

		// После блокировки пакета проверяем аренду в новом снимке данных.
		task.Question, err = tx.Packs.claimClip(ctx, pack.ID, seconds)
		if err != nil {
			return err
		}

		return execute(ctx, tx.db, `UPDATE quiz_packs SET status='pending' WHERE id=$1`, []any{pack.ID})
	})

	return task, err
}

func (r PackRepository) claimClip(ctx context.Context, packID uuid.UUID, seconds int) (domain.Question, error) {
	return one[domain.Question](ctx, r.db, `UPDATE quiz_questions SET status='pending',
		clip_lease_until=clock_timestamp()+make_interval(secs => $2::int)
		WHERE id=(SELECT q.id FROM quiz_packs p JOIN quiz_rounds r ON r.pack_id=p.id
			JOIN quiz_questions q ON q.round_id=r.id
			WHERE p.id=$1 AND q.status<>'ready'
			AND NOT EXISTS (SELECT 1 `+incompleteQuestions+` AND (q.clip_lease_until>now() OR q.clip_error<>''))
			ORDER BY q.number LIMIT 1)
		AND NOT deleted
		RETURNING *`, []any{packID, seconds})
}

// FinishQuizClip завершает актуальную попытку и атомарно обновляет готовность пакета.
func (u *UnitOfWork) FinishQuizClip(ctx context.Context, task QuizClipTask, name, message string) error {
	return u.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx *UnitOfWork) error {
		if _, err := one[struct{ ID uuid.UUID }](ctx, tx.db, `SELECT id FROM quiz_packs WHERE id=$1 FOR UPDATE`,
			[]any{task.PackID}); err != nil {
			return err
		}

		if err := execute(ctx, tx.db, `UPDATE quiz_questions
			SET status=CASE WHEN $3='' THEN 'new' ELSE 'ready' END, clip_name=$3, clip_error=$4, clip_lease_until=NULL
			WHERE id=$1 AND status='pending' AND clip_lease_until=$2`,
			[]any{task.Question.ID, task.Question.ClipLeaseUntil, name, message}); err != nil {
			return err
		}

		return tx.Packs.updateClipStatus(ctx, task.PackID, message)
	})
}

func (r PackRepository) updateClipStatus(ctx context.Context, packID uuid.UUID, message string) error {
	return execute(ctx, r.db, `UPDATE quiz_packs p SET status=CASE
			WHEN $2<>'' THEN 'new'
			WHEN NOT EXISTS (SELECT 1 `+incompleteQuestions+`) THEN 'ready'
			ELSE p.status END
		WHERE p.id=$1`, []any{packID, message})
}

// RetryClips возвращает остановленный пакет в очередь, сохраняя готовые клипы.
func (r PackRepository) RetryClips(ctx context.Context, packID uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE quiz_questions q SET clip_error='' FROM quiz_rounds r
		WHERE q.round_id=r.id AND r.pack_id=$1 AND q.clip_error<>''`, []any{packID})
}
