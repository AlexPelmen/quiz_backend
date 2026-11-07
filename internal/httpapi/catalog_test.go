package httpapi_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestUploadedMP3BecomesPreferred(t *testing.T) {
	pool, uow := testutil.Database(t)

	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("для проверки MP3 требуется ffprobe")
	}

	path := httpAudioFixture(t)
	if path == "" {
		t.Skip("для проверки MP3 требуется ffmpeg")
	}

	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	settings := domain.DefaultSettings()
	settings.StorageDir = t.TempDir()

	if err := uow.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}

	seedHTTPTrack(t, pool, 1)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.Router(service.New(uow, logger, ""), logger)
	call := client(t, router)

	var tracks []domain.TrackWithStatus

	decodeResult(t, call("GET", "/api/v1/tracks?status=finished", "", 200), &tracks)

	if len(tracks) != 1 || tracks[0].Status != "finished" {
		t.Fatalf("не найден трек с готовой записью: %+v", tracks)
	}

	trackPath := "/api/v1/tracks/" + tracks[0].ID.String() + "/audio"
	old := uploadRecording(t, router, trackPath, audio)
	latest := uploadRecording(t, router, trackPath, audio)

	candidate, err := uow.Packs.Pick(ctx, []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track"})
	if err != nil || candidate.Sound == nil || candidate.Sound.ID != latest.ID {
		t.Fatalf("ожидалась последняя загруженная запись: %+v, %v", candidate.Sound, err)
	}

	got := call("GET", "/api/v1/downloads/"+old.ID.String()+"/file", "", 200)
	if !bytes.Equal(got, audio) {
		t.Fatal("предыдущая запись стала недоступна после замены")
	}
}

func uploadRecording(t *testing.T, router http.Handler, path string, audio []byte) domain.Download {
	t.Helper()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(audio))
	req.Header.Set("Content-Type", "application/octet-stream")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("загрузка MP3: %d %s", recorder.Code, recorder.Body.String())
	}

	var uploaded domain.Download

	decodeResult(t, recorder.Body.Bytes(), &uploaded)

	return uploaded
}
