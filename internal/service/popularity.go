package service

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// RoundResultForm передаёт отметку по каждому вопросу и итог раунда.
type RoundResultForm struct {
	AttemptID uuid.UUID           `json:"attempt_id"`
	Correct   *int                `json:"r"`
	Total     int                 `json:"n"`
	Answers   map[uuid.UUID]*bool `json:"answers"`
}

// RecordRoundResult сохраняет попытку и обновляет известность в одной транзакции.
func (a *App) RecordRoundResult(ctx context.Context, id uuid.UUID, f RoundResultForm) error {
	if f.AttemptID == uuid.Nil || f.Correct == nil || f.Total < 1 || len(f.Answers) != f.Total {
		return fmt.Errorf("%w: укажите попытку, итог и все ответы раунда", domain.ErrInvalid)
	}

	return a.uow.RoundTransaction(ctx, func(tx *store.UnitOfWork) error {
		questions, err := tx.Packs.RoundQuestions(ctx, id)
		if err != nil {
			return err
		}

		answers, err := verifyAnswers(questions, f)
		if err != nil {
			return err
		}

		inserted, err := tx.Packs.SaveRoundResult(ctx, store.RoundResult{
			RoundID: id, AttemptID: f.AttemptID, Correct: *f.Correct, Answers: answers,
		})
		if err != nil || !inserted {
			return err
		}

		// Раунды с общими исполнителями обновляют строки в одном порядке.
		slices.SortFunc(questions, func(a, b domain.Question) int { return bytes.Compare(a.ArtistID[:], b.ArtistID[:]) })

		for _, q := range questions {
			if err := tx.Packs.CountAnswer(ctx, q, *f.Answers[q.ID]); err != nil {
				return fmt.Errorf("вопрос %s: %w", q.ID, err)
			}
		}

		return nil
	})
}

func verifyAnswers(questions []domain.Question, f RoundResultForm) ([]bool, error) {
	if len(questions) != f.Total {
		return nil, fmt.Errorf("%w: N не совпадает с числом вопросов в раунде", domain.ErrInvalid)
	}

	answers := make([]bool, 0, len(questions))
	correct := 0

	for _, q := range questions {
		answer := f.Answers[q.ID]
		if answer == nil {
			return nil, fmt.Errorf("%w: отсутствует отметка для вопроса %s", domain.ErrInvalid, q.ID)
		}

		answers = append(answers, *answer)

		if *answer {
			correct++
		}
	}

	if correct != *f.Correct {
		return nil, fmt.Errorf("%w: R не совпадает с числом правильных ответов", domain.ErrInvalid)
	}

	return answers, nil
}
