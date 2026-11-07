package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// PackForm задаёт параметры нового пакета или его описания.
type PackForm struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ArtistTag   string   `json:"artist_tag"`
}

// Generate собирает полные раунды; не обращается к внешним источникам.
func (a *App) Generate(ctx context.Context, f PackForm) (domain.QuizPack, error) {
	p := domain.QuizPack{ID: uuid.New(), Name: strings.TrimSpace(f.Name), Description: f.Description, Tags: f.Tags}
	if p.Tags == nil {
		p.Tags = []string{}
	}

	if err := validateNamePoints(p.Name, 100); err != nil {
		return p, err
	}

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		s, err := tx.Settings.Get(ctx)
		if err != nil {
			return err
		}

		used := []uuid.UUID{}
		for _, kind := range s.RoundTypes {
			round, err := buildRound(ctx, tx, roundInput{Kind: kind, Tag: f.ArtistTag, Used: used, Pack: p})
			if errors.Is(err, domain.ErrNotFound) {
				break
			}

			if err != nil {
				return err
			}

			p.Rounds = append(p.Rounds, round)

			for _, q := range round.Questions {
				used = append(used, q.ArtistID)
			}
		}

		if len(p.Rounds) == 0 {
			return domain.ErrInsufficient
		}

		return tx.Packs.Create(ctx, p)
	})
	if err != nil {
		return p, err
	}

	return a.uow.Packs.Get(ctx, p.ID)
}

type roundInput struct {
	Kind string
	Tag  string
	Used []uuid.UUID
	Pack domain.QuizPack
}

func buildRound(ctx context.Context, tx *store.UnitOfWork, in roundInput) (domain.QuizRound, error) {
	r := domain.QuizRound{ID: uuid.New(), PackID: in.Pack.ID, Number: len(in.Pack.Rounds) + 1, Type: in.Kind}

	used := append([]uuid.UUID{}, in.Used...)
	for i := range 10 {
		c, err := tx.Packs.Pick(ctx, used, store.PickFilter{Kind: in.Kind, Tag: in.Tag})
		if err != nil {
			return r, err
		}

		q, err := makeQuestion(in.Kind, c)
		if err != nil {
			return r, err
		}

		q.ID, q.RoundID, q.Number = uuid.New(), r.ID, (r.Number-1)*10+i+1
		r.Questions = append(r.Questions, q)
		used = append(used, c.Artist.ID)
	}

	return r, nil
}

type roundBuilder func(domain.Candidate) domain.Question

var roundBuilders = map[string]roundBuilder{
	"artist_by_track": artistByTrack,
	"artist_by_cover": artistByCover,
	"track_vowels":    trackVowels,
	"artist_by_logo":  artistByLogo,
	"artist_initial":  artistInitial,
}

func makeQuestion(kind string, c domain.Candidate) (domain.Question, error) {
	builder, ok := roundBuilders[kind]
	if !ok {
		return domain.Question{}, fmt.Errorf("%w: неизвестный раунд", domain.ErrInvalid)
	}

	q := builder(c)
	q.ArtistID = c.Artist.ID
	q.AnswerInfo = map[string]any{"artist": c.Artist.Name, "track": c.Track.Name, "album": c.Album.Name}

	if kind == "artist_by_cover" {
		q.AlbumID = &c.Album.ID
	}

	if kind == "artist_by_cover" || kind == "artist_by_logo" {
		return q, nil
	}

	if c.Sound == nil {
		return q, domain.ErrNotFound
	}

	duration, ok := c.Sound.Meta["duration_seconds"].(float64)
	if !ok || duration <= 0 || math.IsInf(duration, 0) || math.IsNaN(duration) {
		return q, fmt.Errorf("%w: неизвестна длительность MP3", domain.ErrInvalid)
	}

	start, finish := ClipBounds(duration, rand.Float64())
	q.TrackID = &c.Track.ID
	q.SoundDownloadID, q.TrackStartSec, q.TrackFinishSec = &c.Sound.ID, &start, &finish

	return q, nil
}

// ClipBounds возвращает целую короткую запись или случайный интервал ровно 30 секунд.
func ClipBounds(duration, random float64) (float64, float64) {
	if duration <= 30 {
		return 0, duration
	}

	start := math.Floor(random * (duration - 30))

	return start, start + 30
}

func artistByTrack(c domain.Candidate) domain.Question {
	return domain.Question{PrimaryText: "Назовите исполнителя", Answer: c.Artist.Name}
}

func artistByCover(c domain.Candidate) domain.Question {
	return domain.Question{
		PrimaryText: "Назовите исполнителя по обложке альбома",
		Answer:      c.Artist.Name, ImageDownloadID: c.Album.CoverDownloadID,
	}
}

func trackVowels(c domain.Candidate) domain.Question {
	return domain.Question{PrimaryText: "Назовите трек", SecondaryText: vowelHint(c.Track.Name), Answer: c.Track.Name}
}

var parenthetical = regexp.MustCompile(`\([^()]*\)`)

func vowelHint(name string) string {
	for parenthetical.MatchString(name) {
		name = parenthetical.ReplaceAllString(name, "")
	}
	name = strings.Join(strings.Fields(name), " ")
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) && !strings.ContainsRune("аеёиоуыэюяaeiouy", unicode.ToLower(r)) {
			return '_'
		}

		return r
	}, name)
}

func artistByLogo(c domain.Candidate) domain.Question {
	return domain.Question{
		PrimaryText: "Назовите исполнителя по изображению",
		Answer:      c.Artist.Name, ImageDownloadID: c.Artist.LogoDownloadID,
	}
}

func artistInitial(c domain.Candidate) domain.Question {
	name := strings.TrimSpace(c.Artist.Name)
	parts := strings.Fields(name)
	if len(parts) > 1 && strings.EqualFold(parts[0], "the") {
		name = parts[1]
	}
	letter, _ := utf8.DecodeRuneInString(name)
	return domain.Question{PrimaryText: "Назовите исполнителя", SecondaryText: string(letter), Answer: c.Artist.Name}
}

// PackPatch задаёт частичное изменение описания пакета.
type PackPatch struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
}

// PatchPack сохраняет вопросы и выбранные интервалы при изменении описания пакета.
func (a *App) PatchPack(ctx context.Context, id uuid.UUID, f PackPatch) (domain.QuizPack, error) {
	var p domain.QuizPack

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		var err error

		p, err = tx.Packs.Get(ctx, id)
		if err != nil {
			return err
		}

		if f.Name != nil {
			p.Name = strings.TrimSpace(*f.Name)
		}

		if f.Description != nil {
			p.Description = *f.Description
		}

		if f.Tags != nil {
			p.Tags = *f.Tags
		}

		if err := validateNamePoints(p.Name, 100); err != nil {
			return err
		}

		return tx.Packs.Update(ctx, p)
	})
	if err != nil {
		return p, err
	}

	return a.uow.Packs.Get(ctx, id)
}
