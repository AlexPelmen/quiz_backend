package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

func TestInvalidQuestionFeedback(t *testing.T) {
	app := &App{}
	for _, form := range []FeedbackForm{{Type: "like", Comment: "test"}, {Type: "bug", Comment: "  "}, {Type: "bad_audio", Comment: strings.Repeat("x", 4001)}} {
		if err := app.AddQuestionReport(context.Background(), uuid.New(), uuid.New(), form); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ожидалась ошибка валидации: %v", err)
		}
	}
}
