package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// FeedbackQuestion содержит снимок вопроса на момент отправки жалобы.
type FeedbackQuestion struct {
	PackName       string          `json:"pack_name"`
	Number         int             `json:"number"`
	RoundType      string          `json:"round_type"`
	PrimaryText    string          `json:"primary_text"`
	SecondaryText  string          `json:"secondary_text"`
	Answer         string          `json:"answer"`
	ArtistID       *uuid.UUID      `json:"artist_id"`
	TrackID        *uuid.UUID      `json:"track_id"`
	ArtistName     string          `json:"artist_name"`
	TrackName      string          `json:"track_name"`
	AlbumName      string          `json:"album_name"`
	YTMusicURL     string          `json:"yt_music_url"`
	Albums         []FeedbackAlbum `json:"albums"`
	Coartists      []string        `json:"coartists"`
	ReleaseDate    string          `json:"release_date"`
	TrackStartSec  *float64        `json:"track_start_sec"`
	TrackFinishSec *float64        `json:"track_finish_sec"`
	HasAudio       bool            `json:"has_audio"`
	HasImage       bool            `json:"has_image"`
	HasAnswerImage bool            `json:"has_answer_image"`
}

// FeedbackAlbum указывает на альбом, который был показан в вопросе.
type FeedbackAlbum struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// FeedbackReport описывает жалобу и связанный с ней вопрос.
type FeedbackReport struct {
	ID              uuid.UUID        `json:"id"`
	QuestionID      uuid.UUID        `json:"question_id"`
	UserID          uuid.UUID        `json:"user_id"`
	UserName        string           `json:"user_name"`
	Type            string           `json:"type"`
	Comment         string           `json:"comment"`
	CreatedAt       time.Time        `json:"created_at"`
	QuestionDeleted bool             `json:"question_deleted"`
	Question        FeedbackQuestion `json:"question"`
}

// FeedbackLike описывает понравившийся вопрос и количество лайков.
type FeedbackLike struct {
	ID       uuid.UUID        `json:"id"`
	Likes    int64            `json:"likes"`
	Question FeedbackQuestion `json:"question"`
}

const reportSelect = `SELECT f.id,f.question_id,f.user_id,u.name AS user_name,f.type,f.comment,f.created_at,
 q.deleted AS question_deleted,f.snapshot AS question
 FROM question_feedback f JOIN users u ON u.id=f.user_id JOIN quiz_questions q ON q.id=f.question_id
 WHERE f.type<>'like'`

// FeedbackReports возвращает страницу отдельных жалоб в порядке поступления.
func (u *UnitOfWork) FeedbackReports(ctx context.Context, p domain.Page) ([]FeedbackReport, int64, error) {
	var total int64
	if err := u.db.QueryRow(ctx, `SELECT count(*) FROM question_feedback WHERE type<>'like'`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := query[FeedbackReport](ctx, u.db, reportSelect+
		` ORDER BY f.created_at DESC,f.id DESC LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset})

	return rows, total, err
}

// FeedbackLikes возвращает только вопросы, на которые поставили лайки.
func (u *UnitOfWork) FeedbackLikes(ctx context.Context, p domain.Page) ([]FeedbackLike, int64, error) {
	var total int64

	err := u.db.QueryRow(ctx, `SELECT count(DISTINCT question_id) FROM question_feedback WHERE type='like'`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := query[FeedbackLike](ctx, u.db, `SELECT f.question_id AS id,f.likes,to_jsonb(q) AS question FROM (
		SELECT question_id,count(*) AS likes,max(created_at) AS latest FROM question_feedback WHERE type='like'
		GROUP BY question_id ORDER BY latest DESC,question_id LIMIT $1 OFFSET $2
	) f JOIN question_feedback_context q ON q.question_id=f.question_id ORDER BY f.latest DESC,f.question_id`,
		[]any{p.Limit, p.Offset})
	return rows, total, err
}

// FeedbackReport возвращает жалобу, при обработке блокируя её от повторного действия.
func (u *UnitOfWork) FeedbackReport(ctx context.Context, id uuid.UUID, lock bool) (FeedbackReport, error) {
	sql := reportSelect + " AND f.id=$1"
	if lock {
		sql += " FOR UPDATE OF f"
	}

	return one[FeedbackReport](ctx, u.db, sql, []any{id})
}

// FeedbackMedia содержит ссылки на сохранённые медиа вопроса и карточки ответа.
type FeedbackMedia struct {
	ImageDownloadID       *uuid.UUID
	AnswerImageDownloadID *uuid.UUID
	CoverRectangles       []domain.CoverRectangle
	ClipName              string
}

// FeedbackMedia возвращает медиаданные только снимка указанной жалобы.
func (u *UnitOfWork) FeedbackMedia(ctx context.Context, id uuid.UUID) (FeedbackMedia, error) {
	return one[FeedbackMedia](ctx, u.db, `SELECT (snapshot->>'image_download_id')::uuid AS image_download_id,
	    (snapshot->>'answer_image_download_id')::uuid AS answer_image_download_id,
	    snapshot->'cover_rectangles' AS cover_rectangles,snapshot->>'clip_name' AS clip_name
	    FROM question_feedback WHERE id=$1 AND type<>'like'`, []any{id})
}

// AddQuestionReport сохраняет жалобу для существующего вопроса.
func (u *UnitOfWork) AddQuestionReport(ctx context.Context, question, user uuid.UUID, kind, comment string) error {
	return execute(ctx, u.db, `INSERT INTO question_feedback(id,question_id,user_id,type,comment,snapshot)
		SELECT $1,q.question_id,$3,$4,$5,to_jsonb(q) FROM question_feedback_context q
		WHERE q.question_id=$2 AND NOT q.deleted`,
		[]any{uuid.New(), question, user, kind, comment})
}

// QuestionLiked возвращает состояние лайка текущего пользователя.
func (u *UnitOfWork) QuestionLiked(ctx context.Context, question, user uuid.UUID) (bool, error) {
	var liked bool

	err := u.db.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM question_feedback WHERE question_id=q.id AND user_id=$2 AND type='like'
 ) FROM quiz_questions q WHERE q.id=$1 AND NOT q.deleted`, question, user).Scan(&liked)
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
		return execute(ctx, u.db, `INSERT INTO question_feedback(id,question_id,user_id,type)
   SELECT $1,id,$3,'like' FROM quiz_questions WHERE id=$2 AND NOT deleted
   ON CONFLICT (question_id,user_id) WHERE type='like' DO UPDATE SET updated_at=question_feedback.updated_at`, []any{uuid.New(), question, user})
	}
	if _, err := u.QuestionLiked(ctx, question, user); err != nil {
		return err
	}
	_, err := u.db.Exec(ctx, `DELETE FROM question_feedback WHERE question_id=$1 AND user_id=$2 AND type='like'`, question, user)
	return translate(err)
}

// DeleteReport удаляет обработанную жалобу.
func (u *UnitOfWork) DeleteReport(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, u.db, `DELETE FROM question_feedback WHERE id=$1 AND type<>'like'`, []any{id})
}
