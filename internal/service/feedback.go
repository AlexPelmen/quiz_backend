package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// FeedbackForm содержит тип и текст жалобы игрока.
type FeedbackForm struct {
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

// AddQuestionReport проверяет и сохраняет жалобу пользователя.
func (a *App) AddQuestionReport(ctx context.Context, question, user uuid.UUID, f FeedbackForm) error {
	switch f.Type {
	case "question_error", "bad_audio", "bug":
	default:
		return fmt.Errorf("%w: неизвестный тип жалобы", domain.ErrInvalid)
	}
	f.Comment = strings.TrimSpace(f.Comment)
	if !utf8.ValidString(f.Comment) || utf8.RuneCountInString(f.Comment) < 1 || utf8.RuneCountInString(f.Comment) > 4000 {
		return fmt.Errorf("%w: комментарий должен содержать от 1 до 4000 символов", domain.ErrInvalid)
	}
	return a.uow.AddQuestionReport(ctx, question, user, f.Type, f.Comment)
}

// QuestionLiked возвращает состояние лайка пользователя.
func (a *App) QuestionLiked(ctx context.Context, question, user uuid.UUID) (bool, error) {
	return a.uow.QuestionLiked(ctx, question, user)
}

// SetQuestionLike устанавливает лайк пользователя.
func (a *App) SetQuestionLike(ctx context.Context, question, user uuid.UUID, liked bool) error {
	return a.uow.SetQuestionLike(ctx, question, user, liked)
}

// FeedbackQuestions возвращает вопросы с показателями обратной связи.
func (a *App) FeedbackQuestions(ctx context.Context, filter string, p domain.Page) ([]store.FeedbackQuestion, int64, error) {
	switch filter {
	case "", "reports", "unread", "likes":
	default:
		return nil, 0, domain.ErrInvalid
	}
	return a.uow.FeedbackQuestions(ctx, filter, p.Limit, p.Offset)
}

// QuestionReports возвращает жалобы по вопросу.
func (a *App) QuestionReports(ctx context.Context, id uuid.UUID) ([]store.FeedbackEntry, error) {
	return a.uow.QuestionReports(ctx, id)
}

// SetReportViewed меняет отметку просмотра жалобы.
func (a *App) SetReportViewed(ctx context.Context, id uuid.UUID, viewed bool) error {
	return a.uow.SetReportViewed(ctx, id, viewed)
}
