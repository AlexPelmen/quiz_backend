package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// PlayingPack возвращает готовый пакет без ответов и ссылок на задуманные объекты.
func (a *App) PlayingPack(ctx context.Context, id uuid.UUID) (domain.QuizPack, error) {
	pack, err := a.uow.Packs.Get(ctx, id)
	if err != nil {
		return pack, err
	}

	if pack.Status != "ready" {
		return pack, domain.ErrNotFound
	}

	for _, round := range pack.Rounds {
		for i := range round.Questions {
			q := &round.Questions[i]
			if !q.Deleted && round.Type == "artist_initial" {
				q.SecondaryText = initialHint(cleanQuestionName(q.Answer))
			}

			q.Answer, q.AnswerInfo = "", nil
			q.ArtistID, q.TrackID = uuid.Nil, nil
		}
	}

	return pack, nil
}

// FindAnswerOptions возвращает подсказки для поля ответа по игровому каталогу.
func (a *App) FindAnswerOptions(ctx context.Context, name, preferred string) ([]domain.AnswerOption, error) {
	name = norm.NFC.String(strings.TrimSpace(name))
	length := utf8.RuneCountInString(name)

	if length > 1000 || strings.ContainsRune(name, 0) || (preferred != "" && !answerOptionType(preferred)) {
		return nil, domain.ErrInvalid
	}

	if length == 0 {
		return []domain.AnswerOption{}, nil
	}

	return a.uow.Packs.AnswerOptions(ctx, name, preferred)
}

func answerOptionType(kind string) bool {
	return kind == "artist" || kind == "album" || kind == "track"
}

// AnswerForm передаёт тип и идентификатор выбранной подсказки.
type AnswerForm struct {
	AttemptID  uuid.UUID `json:"attempt_id"`
	OptionType string    `json:"option_type"`
	OptionID   uuid.UUID `json:"option_id"`
}

// AnswerReveal содержит принятую отправку и данные карточки правильного ответа.
type AnswerReveal struct {
	domain.SubmittedAnswer
	Question domain.Question `json:"question"`
}

// SubmitQuestionAnswer проверяет и фиксирует ответ игрока перед раскрытием карточки.
func (a *App) SubmitQuestionAnswer(
	ctx context.Context, questionID, userID uuid.UUID, f AnswerForm,
) (AnswerReveal, error) {
	if f.AttemptID == uuid.Nil {
		return AnswerReveal{}, fmt.Errorf("%w: укажите попытку", domain.ErrInvalid)
	}

	if f.OptionID == uuid.Nil || !answerOptionType(f.OptionType) {
		return AnswerReveal{}, fmt.Errorf("%w: выберите вариант из списка", domain.ErrInvalid)
	}

	var response AnswerReveal

	err := a.uow.RoundTransaction(ctx, func(tx *store.UnitOfWork) error {
		q, err := tx.Packs.PlayableQuestion(ctx, questionID, f.OptionType, f.OptionID)
		if err != nil {
			return err
		}

		if q.OptionName == nil {
			return fmt.Errorf("%w: вариант больше недоступен, выберите другой", domain.ErrInvalid)
		}

		keys := domain.QuestionAttempt{QuestionID: questionID, UserID: userID, AttemptID: f.AttemptID}
		answer := domain.SubmittedAnswer{Text: *q.OptionName, Correct: matchesAnswer(q, f)}
		saved, err := tx.Packs.SaveQuestionAnswer(ctx, keys, answer)
		response = AnswerReveal{SubmittedAnswer: saved, Question: q.Question}

		return err
	})

	return response, err
}

func matchesAnswer(q store.PlayableQuestion, f AnswerForm) bool {
	if q.RoundType == "track_vowels" {
		return f.OptionType == "track" && q.TrackID != nil && (f.OptionID == *q.TrackID || q.OptionNearAnswer)
	}

	return f.OptionType == "artist" && (f.OptionID == q.ArtistID || q.OptionNearAnswer)
}
