package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// FeedbackQuestion описывает вопрос и итоговые показатели обратной связи.
type FeedbackQuestion struct {
	ID              uuid.UUID  `json:"id"`
	PackID          uuid.UUID  `json:"pack_id"`
	PackName        string     `json:"pack_name"`
	Number          int        `json:"number"`
	RoundType       string     `json:"round_type"`
	PrimaryText     string     `json:"primary_text"`
	Answer          string     `json:"answer"`
	HasAudio        bool       `json:"has_audio"`
	ImageDownloadID *uuid.UUID `json:"image_download_id"`
	Reports         int64      `json:"reports"`
	Unread          int64      `json:"unread"`
	Likes           int64      `json:"likes"`
}

// FeedbackEntry описывает жалобу на вопрос.
type FeedbackEntry struct {
	ID         uuid.UUID `json:"id"`
	QuestionID uuid.UUID `json:"question_id"`
	UserID     uuid.UUID `json:"user_id"`
	UserName   string    `json:"user_name"`
	Type       string    `json:"type"`
	Comment    string    `json:"comment"`
	Viewed     bool      `json:"viewed"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const feedbackQuestionsFrom = `FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id JOIN quiz_packs p ON p.id=r.pack_id
 LEFT JOIN LATERAL (SELECT count(*) FILTER (WHERE type<>'like') AS reports,
 count(*) FILTER (WHERE type<>'like' AND NOT viewed) AS unread,
 count(*) FILTER (WHERE type='like') AS likes FROM question_feedback WHERE question_id=q.id) f ON true
 WHERE ($1='' OR ($1='reports' AND f.reports>0) OR ($1='unread' AND f.unread>0) OR ($1='likes' AND f.likes>0))`

// FeedbackQuestions возвращает страницу вопросов и их общее число с фильтром.
func (u *UnitOfWork) FeedbackQuestions(ctx context.Context, filter string, limit, offset int) ([]FeedbackQuestion, int64, error) {
	var total int64
	if err := u.db.QueryRow(ctx, `SELECT count(*) `+feedbackQuestionsFrom, filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := query[FeedbackQuestion](ctx, u.db, `SELECT q.id,p.id AS pack_id,p.name AS pack_name,q.number,r.type AS round_type,
 q.primary_text,q.answer,(q.sound_download_id IS NOT NULL) AS has_audio,q.image_download_id,f.reports,f.unread,f.likes `+feedbackQuestionsFrom+` ORDER BY q.created_at DESC,q.id DESC LIMIT $2 OFFSET $3`, []any{filter, limit, offset})
	return rows, total, err
}

// QuestionReports возвращает жалобы и имена их авторов.
func (u *UnitOfWork) QuestionReports(ctx context.Context, id uuid.UUID) ([]FeedbackEntry, error) {
	return query[FeedbackEntry](ctx, u.db, `SELECT f.id,f.question_id,f.user_id,u.name AS user_name,f.type,f.comment,f.viewed,f.created_at,f.updated_at
 FROM question_feedback f JOIN users u ON u.id=f.user_id WHERE f.question_id=$1 AND f.type<>'like' ORDER BY f.created_at DESC,f.id DESC`, []any{id})
}

// AddQuestionReport сохраняет жалобу для существующего вопроса.
func (u *UnitOfWork) AddQuestionReport(ctx context.Context, question, user uuid.UUID, kind, comment string) error {
	return execute(ctx, u.db, `INSERT INTO question_feedback(id,question_id,user_id,type,comment)
 SELECT $1,id,$3,$4,$5 FROM quiz_questions WHERE id=$2`, []any{uuid.New(), question, user, kind, comment})
}

// QuestionLiked возвращает состояние лайка текущего пользователя.
func (u *UnitOfWork) QuestionLiked(ctx context.Context, question, user uuid.UUID) (bool, error) {
	var liked bool
	err := u.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM question_feedback WHERE question_id=q.id AND user_id=$2 AND type='like') FROM quiz_questions q WHERE q.id=$1`, question, user).Scan(&liked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, translate(err)
	}
	return liked, nil
}

// SetQuestionLike устанавливает или снимает лайк пользователя.
func (u *UnitOfWork) SetQuestionLike(ctx context.Context, question, user uuid.UUID, liked bool) error {
	if liked {
		return execute(ctx, u.db, `INSERT INTO question_feedback(id,question_id,user_id,type) SELECT $1,id,$3,'like' FROM quiz_questions WHERE id=$2
   ON CONFLICT (question_id,user_id) WHERE type='like' DO UPDATE SET updated_at=question_feedback.updated_at`, []any{uuid.New(), question, user})
	}
	if _, err := u.QuestionLiked(ctx, question, user); err != nil {
		return err
	}
	_, err := u.db.Exec(ctx, `DELETE FROM question_feedback WHERE question_id=$1 AND user_id=$2 AND type='like'`, question, user)
	return translate(err)
}

// SetReportViewed меняет отметку просмотра жалобы.
func (u *UnitOfWork) SetReportViewed(ctx context.Context, id uuid.UUID, viewed bool) error {
	return execute(ctx, u.db, `UPDATE question_feedback SET viewed=$2,updated_at=now() WHERE id=$1 AND type<>'like'`, []any{id, viewed})
}
