package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/objectstorage"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestS3AudioFromUpload(t *testing.T) {
	if os.Getenv("QUIZ_TEST_S3") != "1" {
		t.Skip("для теста с настроенным бакетом требуется QUIZ_TEST_S3=1")
	}

	bucket, err := objectstorage.FromEnv()
	if err != nil {
		t.Fatal(err)
	}

	filename := httpAudioFixture(t)
	if filename == "" {
		t.Skip("требуется ffmpeg")
	}

	audio, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}

	pool, uow := testutil.Database(t)
	ctx := t.Context()
	settings := domain.DefaultSettings()
	settings.StorageDir = filepath.Join(t.TempDir(), "unused")

	if err := uow.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}

	seedHTTPTrack(t, pool, 1)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := service.New(uow, logger, "")
	app.UseObjectStorage(bucket)

	router := httpapi.Router(app, logger)

	var artist, track uuid.UUID

	if err := pool.QueryRow(ctx, "SELECT artist_id, track_id FROM artist_to_track").Scan(&artist, &track); err != nil {
		t.Fatal(err)
	}

	download := uploadRecording(t, router, "/api/v1/tracks/"+track.String()+"/audio", audio)

	key, err := bucket.Key(download.URI)
	if err != nil {
		t.Fatal(err)
	}

	removeTestObject(t, bucket, key)
	assertS3Range(t, router, "/api/v1/downloads/"+download.ID.String()+"/file", audio[:32])

	pack, clipKey := testS3Pack(t, artist, track, download.ID)
	if err := uow.Packs.Create(ctx, pack); err != nil {
		t.Fatal(err)
	}

	removeTestObject(t, bucket, clipKey)

	url := "/api/v1/quiz-packs/" + pack.ID.String()

	defer startQuizWorkers(t, app)()

	waitForQuizPack(t, client(t, router), url, func(pack domain.QuizPack) bool { return pack.Status == "ready" })

	route := url + "/questions/1/audio"

	clip := client(t, router)("GET", route, "", 200)
	if len(clip) <= 32 {
		t.Fatalf("пустой MP3 фрагмент: %d байт", len(clip))
	}

	assertS3Range(t, router, route, clip[:32])

	result, err := app.StorageAction(ctx, "delete", []string{key})
	if err != nil || len(result) != 1 || result[0].Error == "" {
		t.Fatalf("используемый трек должен быть защищён: %v, %v", result, err)
	}

	if _, err := os.Stat(settings.StorageDir); !os.IsNotExist(err) {
		t.Fatalf("режим S3 использовал каталог медиатеки: %v", err)
	}
}

func assertS3Range(t *testing.T, router http.Handler, route string, want []byte) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, route, nil)
	req.Header.Set("Range", "bytes=0-31")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusPartialContent || recorder.Header().Get("Content-Type") != "audio/mpeg" ||
		recorder.Header().Get("Content-Range") == "" || !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("неверный ответ Range: %d, %v, размер %d", recorder.Code, recorder.Header(), recorder.Body.Len())
	}
}

func testS3Pack(t *testing.T, artist, track, download uuid.UUID) (domain.QuizPack, string) {
	t.Helper()

	start, finish := 0.0, 2.0
	round := domain.QuizRound{ID: uuid.New(), Number: 1, Type: "artist_by_track"}
	question := domain.Question{
		ID: uuid.New(), RoundID: round.ID, Number: 1, ArtistID: artist, TrackID: &track,
		SoundDownloadID: &download, TrackStartSec: &start, TrackFinishSec: &finish,
		PrimaryText: "Кто исполнитель?", Answer: "Исполнитель", AnswerInfo: map[string]any{},
	}
	round.Questions = []domain.Question{question}

	return domain.QuizPack{
		ID: uuid.New(), Name: "Проверка S3", Tags: []string{}, Rounds: []domain.QuizRound{round},
	}, "music/clips/" + question.ID.String() + ".mp3"
}

func removeTestObject(t *testing.T, bucket *objectstorage.Bucket, key string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := bucket.Delete(ctx, key); err != nil {
			t.Error(err)
		}
	})
}
