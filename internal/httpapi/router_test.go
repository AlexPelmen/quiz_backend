package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestHTTPWorkflow(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := service.New(uow, logger, "test-token")
	router := httpapi.Router(app, logger)
	call := client(t, router)
	call("POST", "/api/v1/artists", `{"name":"Тест","yt_plays":-1}`, 400)

	created := call("POST", "/api/v1/artists", `{"name":"Тест","tags":["рок"]}`, 201)

	var artist domain.Artist

	decodeResult(t, created, &artist)

	if artist.Asked != 0 || artist.Guessed != 0 || artist.ExcludedFromQuiz {
		t.Fatal("не назначена оценка по умолчанию")
	}

	call("POST", "/api/v1/artists", `{"name":"ТЕСТ"}`, 409)

	url := "/api/v1/artists/" + artist.ID.String()
	call("PATCH", url, `{"excluded_from_quiz":true,"country":"RU"}`, 200)
	call("PATCH", url, `{"status":"manual"}`, 200)

	manual := call("GET", url, "", 200)
	decodeResult(t, manual, &artist)

	if artist.Status != "manual" {
		t.Fatal("статус manual не сохранился")
	}

	call("PATCH", url, `{"status":"parsed"}`, 400)
	call("PATCH", url, `{"status":"created"}`, 200)

	if _, err := pool.Exec(t.Context(), "UPDATE artists SET status='processing' WHERE id=$1", artist.ID); err != nil {
		t.Fatal(err)
	}

	call("PATCH", url, `{"status":"manual"}`, 409)

	if _, err := pool.Exec(t.Context(), "UPDATE artists SET status='created' WHERE id=$1", artist.ID); err != nil {
		t.Fatal(err)
	}

	call("PATCH", url, `{"unknown":1}`, 400)
	call("POST", "/api/v1/quiz-packs", `{"name":"Пустой"}`, 422)
	call("PATCH", "/api/v1/settings", `{"quiz_guess_rate_asked_threshold":-1}`, 400)
	call("PATCH", "/api/v1/settings", `{"quiz_guess_rate_enabled":false}`, 200)
	call("GET", "/api/v1/artists?limit=999", "", 400)
	call("GET", "/api/v1/artists/not-a-uuid", "", 400)
	call("DELETE", url, "", 204)
	call("GET", url, "", 404)
	testPack(t, pool, router)
}

func TestQuotaAPI(t *testing.T) {
	_, uow := testutil.Database(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("не передан токен источнику")
		}

		w.Header().Set("X-Ratelimit-Limit", "10000")
		w.Header().Set("X-Ratelimit-Remaining", "9998")
		w.Header().Set("X-Ratelimit-Reset", "3600")
		w.Header().Set("Content-Type", "application/json")

		if _, err := w.Write([]byte(`{"response":{}}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	s, err := uow.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	s.GeniusURL = server.URL
	if err := uow.Settings.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	app := service.New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "test-token")

	call := client(t, httpapi.Router(app, slog.New(slog.NewTextHandler(io.Discard, nil))))
	if string(call("GET", "/api/v1/genius-quota", "", 200)) != "null" {
		t.Fatal("ожидался неизвестный лимит")
	}

	data := call("POST", "/api/v1/genius-quota/refresh", "", 200)

	var quota domain.GeniusQuota

	decodeResult(t, data, &quota)

	if quota.Limit != 10000 || quota.Remaining != 9998 {
		t.Fatalf("неверный лимит: %+v", quota)
	}

	decodeResult(t, call("GET", "/api/v1/genius-quota", "", 200), &quota)

	if quota.Remaining != 9998 {
		t.Fatal("лимит не сохранился")
	}
}

func client(t *testing.T, handler http.Handler) func(string, string, string, int) []byte {
	return func(method, path, body string, status int) []byte {
		t.Helper()

		r := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(r, req)

		if r.Code != status {
			t.Fatalf("%s %s: ожидали %d, получили %d: %s", method, path, status, r.Code, r.Body.String())
		}

		return r.Body.Bytes()
	}
}

func decodeResult(t *testing.T, data []byte, target any) {
	t.Helper()

	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func testPack(t *testing.T, pool *pgxpool.Pool, router http.Handler) {
	t.Helper()

	call := client(t, router)

	for i := range 17 {
		seedHTTPTrack(t, pool, i)
	}

	data := call("POST", "/api/v1/quiz-packs", `{"name":"Квиз","question_count":10,"round_types":["artist_by_track"]}`, 201)

	var p domain.QuizPack

	decodeResult(t, data, &p)

	if len(p.Rounds) != 1 || len(p.Rounds[0].Questions) != 10 {
		t.Fatalf("ожидался один полный раунд: %+v", p)
	}

	used := map[uuid.UUID]bool{}
	for _, q := range p.Rounds[0].Questions {
		if used[q.ArtistID] || q.TrackStartSec == nil || q.TrackFinishSec == nil {
			t.Fatalf("повтор исполнителя или отсутствуют границы: %+v", q)
		}

		used[q.ArtistID] = true

		if *q.TrackStartSec != 0 || math.Abs(*q.TrackFinishSec-5) > 1e-9 {
			t.Fatalf("короткий трек не включён целиком: %+v", q)
		}
	}

	url := "/api/v1/quiz-packs/" + p.ID.String()
	call("PATCH", url, `{"description":"Новое описание"}`, 200)
	call("GET", url+"/questions/0/audio", "", 400)
	call("GET", url+"/questions/11/audio", "", 404)

	if path := httpAudioFixture(t); path != "" {
		testHTTPAudio(t, pool, router, httpAudioInput{URL: url, Path: path})
	}

	call("DELETE", url, "", 204)
	call("GET", url, "", 404)
	testMultipleRounds(t, pool, router)
}

func testMultipleRounds(t *testing.T, pool *pgxpool.Pool, router http.Handler) {
	t.Helper()

	for i := 17; i < 25; i++ {
		seedHTTPTrack(t, pool, i)
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO album_guessing(album_id,cover_download_id,approved,rectangles)
		SELECT id,cover_download_id,true,'[]' FROM albums`); err != nil {
		t.Fatal(err)
	}

	call := client(t, router)
	data := call("POST", "/api/v1/quiz-packs", `{"name":"Два раунда","question_count":20,"round_types":["artist_by_track","artist_by_cover"]}`, 201)

	var pack domain.QuizPack

	decodeResult(t, data, &pack)

	if len(pack.Rounds) != 2 {
		t.Fatalf("ожидались два полных раунда: %d", len(pack.Rounds))
	}

	used := map[uuid.UUID]bool{}

	for _, round := range pack.Rounds {
		for _, q := range round.Questions {
			if used[q.ArtistID] {
				t.Fatal("исполнитель повторился между раундами")
			}

			used[q.ArtistID] = true
		}
	}

	url := "/api/v1/quiz-packs/" + pack.ID.String()
	call("GET", url+"/questions/11/audio", "", 404)
}

func httpAudioFixture(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Log("аудио-проверка HTTP пропущена: отсутствует ffmpeg")
		return ""
	}

	path := filepath.Join(t.TempDir(), "source.mp3")

	_, err := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "5", path).Output()
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func seedHTTPTrack(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()

	a, al, tr, d, image := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	queries := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO artists(id,name,status) VALUES($1,$2,'parsed')", []any{a, fmt.Sprintf("Исполнитель %d", n)}},
		{
			`WITH saved AS (INSERT INTO albums(id,name) VALUES($1,'Альбом') RETURNING id)
                INSERT INTO artist_to_album(album_id,artist_id) SELECT id,$2 FROM saved`,
			[]any{al, a},
		},
		{
			`WITH saved AS (INSERT INTO tracks(id,name,album_id) VALUES($1,'Трек',$3) RETURNING id)
                INSERT INTO artist_to_track(track_id,artist_id) SELECT id,$2 FROM saved`,
			[]any{tr, a, al},
		},
		{
			"INSERT INTO downloads(id,source_type,status,file_type,meta) VALUES($1,'mp3','finished','mp3',$2)",
			[]any{d, map[string]any{"duration_seconds": 5}},
		},
		{"INSERT INTO track_to_download VALUES($1,$2)", []any{tr, d}},
		{"INSERT INTO downloads(id,source_type,status,file_type) VALUES($1,'image','finished','image')", []any{image}},
		{"UPDATE artists SET logo_download_id=$1 WHERE id=$2", []any{image, a}},
		{"UPDATE albums SET cover_download_id=$1 WHERE id=$2", []any{image, al}},
	}
	for _, q := range queries {
		if _, err := pool.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

type httpAudioInput struct {
	URL  string
	Path string
}

func testHTTPAudio(t *testing.T, pool *pgxpool.Pool, router http.Handler, input httpAudioInput) {
	call := client(t, router)
	url, path := input.URL, input.Path

	t.Helper()

	if _, err := pool.Exec(t.Context(), "UPDATE downloads SET uri=$1", path); err != nil {
		t.Fatal(err)
	}

	data := call("GET", url+"/questions/1/audio", "", 200)
	if len(data) < 1000 {
		t.Fatal("аудио-ручка вернула пустой файл")
	}

	file := filepath.Join(t.TempDir(), "http.mp3")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-show_entries",
		"format=duration", "-of", "json", file).Output()
	if err != nil {
		t.Fatalf("ответ не является MP3: %v %s", err, output)
	}

	response := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url+"/questions/1/audio", nil)
	req.Header.Set("Range", "bytes=0-99")
	router.ServeHTTP(response, req)

	if response.Code != 206 || response.Header().Get("Content-Type") != "audio/mpeg" || response.Body.Len() != 100 {
		t.Fatalf("не поддерживается HTTP Range: %d, %v", response.Code, response.Header())
	}
}
