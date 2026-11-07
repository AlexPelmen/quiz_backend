package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestRoundResults(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.Router(service.New(uow, logger, ""), logger)
	call := client(t, router)

	for i := range 30 {
		seedHTTPTrack(t, pool, i)
	}

	// Альбомы по-прежнему участвуют в раунде обложек, хотя треки не привязаны к ним.
	if _, err := pool.Exec(t.Context(), `UPDATE artists SET points=40;
		UPDATE albums SET points=40; UPDATE tracks SET points=40,album_id=NULL;
		INSERT INTO album_guessing(album_id,cover_download_id,approved,rectangles)
		SELECT id,cover_download_id,true,'[]' FROM albums`); err != nil {
		t.Fatal(err)
	}

	call("PATCH", "/api/v1/settings", `{"round_types":["artist_by_track","artist_by_cover","artist_by_logo",
		"artist_by_track","artist_by_track","artist_by_track","artist_by_track","artist_by_track",
		"artist_by_track","artist_by_track"]}`, 200)

	var pack domain.QuizPack

	decodeResult(t, call("POST", "/api/v1/quiz-packs", `{"name":"Статистика"}`, 201), &pack)

	if len(pack.Rounds) != 3 {
		t.Fatalf("получено раундов: %d, ожидались звук, обложка и логотип", len(pack.Rounds))
	}

	for _, round := range pack.Rounds {
		t.Run(round.Type, func(t *testing.T) { assertRoundPopularity(t, pool, router, round) })
	}
}

type roundPayload struct {
	AttemptID uuid.UUID          `json:"attempt_id"`
	Correct   int                `json:"r"`
	Total     int                `json:"n"`
	Answers   map[uuid.UUID]bool `json:"answers"`
}

func assertRoundPopularity(t *testing.T, pool *pgxpool.Pool, router http.Handler, round domain.QuizRound) {
	t.Helper()

	call := client(t, router)
	url := "/api/v1/quiz-rounds/" + round.ID.String() + "/results"
	payload := roundPayload{AttemptID: uuid.New(), Total: 10, Correct: 5, Answers: map[uuid.UUID]bool{}}

	for i, q := range round.Questions {
		payload.Answers[q.ID] = i < 5
	}

	post := func(status int) {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}

		call("POST", url, string(data), status)
	}

	payload.Correct = 4

	post(400)

	payload.Correct = 5
	first, last := round.Questions[0], round.Questions[9]
	unknown := uuid.New()

	delete(payload.Answers, first.ID)

	payload.Answers[unknown] = true

	post(400)
	delete(payload.Answers, unknown)

	payload.Answers[first.ID] = true

	post(204)
	post(204)

	payload.Answers[first.ID], payload.Answers[last.ID] = false, true

	post(409)

	payload.Answers[first.ID], payload.Answers[last.ID] = true, false

	assertQuestionPopularity(t, pool, expectedPopularity{round.Type, first, 46, 1})
	assertQuestionPopularity(t, pool, expectedPopularity{round.Type, last, 36, 1})

	call("PATCH", "/api/v1/settings", `{"min_popularity_delta":-1}`, 400)
	call("PATCH", "/api/v1/settings", `{"k_popularity":10,"min_popularity_delta":2}`, 200)

	payload.AttemptID = uuid.New()

	post(204)
	assertQuestionPopularity(t, pool, expectedPopularity{round.Type, first, 48, 2})
	assertQuestionPopularity(t, pool, expectedPopularity{round.Type, last, 34, 2})
	call("PATCH", "/api/v1/settings", `{"k_popularity":20,"min_popularity_delta":1}`, 200)
}

type expectedPopularity struct {
	kind  string
	q     domain.Question
	want  float64
	count int
}

func assertQuestionPopularity(t *testing.T, pool *pgxpool.Pool, expected expectedPopularity) {
	t.Helper()

	kind, q := expected.kind, expected.q

	var albumID *uuid.UUID

	if err := pool.QueryRow(t.Context(), "SELECT album_id FROM quiz_questions WHERE id=$1", q.ID).
		Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if (q.TrackID != nil) != (kind == "artist_by_track") || (albumID != nil) != (kind == "artist_by_cover") {
		t.Fatalf("неверная цель вопроса: тип %s, трек %v, альбом %v", kind, q.TrackID, albumID)
	}

	for _, table := range []string{"artists", "albums", "tracks"} {
		sql := "SELECT points,guess_num FROM " + table + " WHERE artist_id=$1"
		if table == "artists" {
			sql = "SELECT points,guess_num FROM artists WHERE id=$1"
		}

		var (
			points float64
			shown  int
		)

		if err := pool.QueryRow(t.Context(), sql, q.ArtistID).Scan(&points, &shown); err != nil {
			t.Fatal(err)
		}

		wantPoints, wantShown := 40.0, 0
		if table == "artists" || (table == "tracks" && q.TrackID != nil) || (table == "albums" && albumID != nil) {
			wantPoints, wantShown = expected.want, expected.count
		}

		if points != wantPoints || shown != wantShown {
			t.Fatalf("%s: points=%v, guess_num=%d; ожидается %v и %d", table, points, shown, wantPoints, wantShown)
		}
	}
}
