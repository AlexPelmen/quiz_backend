package httpapi_test

import (
	"io"
	"log/slog"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
	"github.com/google/uuid"
)

func TestPackQuestionCount(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	for i := range 30 {
		seedHTTPTrack(t, pool, i)
	}
	for _, body := range []string{
		`{"name":"Ноль","question_count":0}`,
		`{"name":"Неполный","question_count":15}`,
		`{"name":"Слишком много","question_count":110}`,
	} {
		call("POST", "/api/v1/quiz-packs", body, 400)
	}
	call("POST", "/api/v1/quiz-packs", `{"name":"По умолчанию","round_types":["artist_by_track"]}`, 422)
	call("POST", "/api/v1/quiz-packs", `{"name":"Мало исполнителей","question_count":40,"round_types":["artist_by_track"]}`, 422)
	var pack domain.QuizPack
	decodeResult(t, call("POST", "/api/v1/quiz-packs", `{"name":"Три раунда","question_count":30,"round_types":["artist_by_track","artist_initial"]}`, 201), &pack)
	if len(pack.Rounds) != 3 {
		t.Fatalf("раундов: %d", len(pack.Rounds))
	}
	if pack.Rounds[0].Type == pack.Rounds[1].Type {
		t.Fatal("тип повторён до использования другого типа")
	}
	used := map[uuid.UUID]bool{}
	for _, round := range pack.Rounds {
		if len(round.Questions) != 10 {
			t.Fatalf("вопросов в раунде: %d", len(round.Questions))
		}
		for _, q := range round.Questions {
			if used[q.ArtistID] {
				t.Fatal("исполнитель повторён")
			}
			used[q.ArtistID] = true
		}
	}
}
