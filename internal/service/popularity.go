package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
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

		s, err := tx.Settings.Get(ctx)
		if err != nil {
			return err
		}

		// Раунды с общими исполнителями берут блокировки каталога в одном порядке.
		slices.SortFunc(questions, func(a, b domain.Question) int { return bytes.Compare(a.ArtistID[:], b.ArtistID[:]) })

		rate := float64(*f.Correct) / float64(f.Total)
		score := roundScore{rate: rate, k: s.KPopularity, minDelta: s.MinPopularityDelta}

		for _, q := range questions {
			score.correct = *f.Answers[q.ID]
			if err := scoreQuestion(ctx, tx.Packs, q, score); err != nil {
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

type roundScore struct {
	correct  bool
	rate     float64
	k        float64
	minDelta float64
}

func scoreQuestion(ctx context.Context, packs store.PackRepository, q domain.Question, score roundScore) error {
	items, err := packs.LockPopularity(ctx, q)
	if err != nil {
		return err
	}

	// Вес пары определяется прошлым числом показов трека или обложки.
	weight := 1 / (math.Log1p(float64(items[len(items)-1].GuessNum)) + 1)
	for _, p := range items {
		p.Points = score.points(p.Points, weight)
		if err := packs.UpdatePopularity(ctx, p); err != nil {
			return err
		}
	}

	return nil
}

func (s roundScore) points(current, weight float64) float64 {
	p := current / 100
	delta := -s.k * s.rate * p

	if s.correct {
		delta = s.k * (1 - s.rate) * (1 - p)
	}

	if delta == 0 {
		return current
	}

	// Автооценка не должна попадать в граничные значения из-за округления.
	delta = math.Copysign(max(s.minDelta, math.Abs(delta)*weight), delta)
	next := min(99.99, max(0.01, math.Round((current+delta)*100)/100))
	if delta > 0 {
		return max(current, next)
	}

	return min(current, next)
}
