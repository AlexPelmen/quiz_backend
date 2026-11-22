package service

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

func (a *App) prepareNextQuizClip(ctx context.Context, s domain.Settings) error {
	task, err := a.uow.ClaimQuizClip(ctx, s.TaskTimeoutSeconds+30)
	if err != nil {
		return err
	}

	clipCtx, cancel := context.WithTimeout(ctx, time.Duration(s.TaskTimeoutSeconds)*time.Second)
	file, err := a.prepareClip(clipCtx, s, task.Question)

	cancel()

	name, message := "", ""

	switch {
	case err == nil:
		name = filepath.Base(file)
	case ctx.Err() == nil:
		message = fmt.Sprintf("Не удалось подготовить клип для вопроса №%d", task.Question.Number)

		a.log.ErrorContext(ctx, "подготовка клипа квиза",
			"pack_id", task.PackID, "question_id", task.Question.ID, "error", err)
	}

	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return a.uow.FinishQuizClip(finishCtx, task, name, message)
}

// RetryQuizClips возобновляет подготовку пакета после ошибки.
func (a *App) RetryQuizClips(ctx context.Context, id uuid.UUID) error {
	return a.uow.Packs.RetryClips(ctx, id)
}
