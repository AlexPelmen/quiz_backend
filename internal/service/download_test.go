package service

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestDirectMP3Download(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("требуется ffmpeg")
	}

	pool, uow := testutil.Database(t)
	s := domain.DefaultSettings()
	s.StorageDir = t.TempDir()
	input := filepath.Join(t.TempDir(), "input.mp3")

	_, err := runCommand(t.Context(), s.FFmpeg, []string{
		"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", input,
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/song.mp3" {
			http.Error(w, "недоступно", http.StatusServiceUnavailable)
			return
		}

		http.ServeFile(w, r, input)
	}))
	defer server.Close()

	d, err := uow.Downloads.Create(t.Context(), domain.Download{
		ID: uuid.New(), SourceType: "mp3",
		SourceURI: server.URL + "/song.mp3", FileType: "mp3",
		Meta: map[string]any{"title": "Песня", "artist": "Исполнитель", "album": "Альбом"},
	})
	if err != nil {
		t.Fatal(err)
	}

	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err := app.downloadNext(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	saved, err := uow.Downloads.Get(t.Context(), d.ID)
	if err != nil || saved.Status != "finished" || saved.Meta["duration_seconds"].(float64) <= 0 {
		t.Fatalf("загрузка не завершена: %+v %v", saved, err)
	}

	if _, err := os.Stat(saved.URI); err != nil {
		t.Fatal(err)
	}

	s.MaxDownloadBytes = 100
	if err := fetchFile(t.Context(), s, server.URL+"/song.mp3", filepath.Join(t.TempDir(), "too-large")); err == nil {
		t.Fatal("не сработало ограничение размера")
	}

	assertRetryLimit(t, app, uow, retryFixture{Pool: pool, Settings: s, URL: server.URL + "/missing"})
}

type retryFixture struct {
	Pool     *pgxpool.Pool
	Settings domain.Settings
	URL      string
}

func assertRetryLimit(t *testing.T, app *App, uow *store.UnitOfWork, in retryFixture) {
	t.Helper()

	d, err := uow.Downloads.Create(t.Context(), domain.Download{
		ID: uuid.New(), SourceType: "mp3",
		SourceURI: in.URL, FileType: "mp3", Meta: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := range 3 {
		if err := app.downloadNext(t.Context(), in.Settings); err != nil {
			t.Fatal(err)
		}

		current, err := uow.Downloads.Get(t.Context(), d.ID)
		if err != nil || current.Attempts != attempt+1 || current.Error == "" {
			t.Fatalf("не записана попытка: %+v, %v", current, err)
		}

		if attempt < 2 && current.Status != "pending" || attempt == 2 && current.Status != "errored" {
			t.Fatalf("неверное состояние после попытки: %+v", current)
		}

		if _, err := in.Pool.Exec(t.Context(), "UPDATE downloads SET next_attempt_at=now() WHERE id=$1", d.ID); err != nil {
			t.Fatal(err)
		}
	}

	if err := app.downloadNext(t.Context(), in.Settings); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("исчерпанная задача осталась в очереди: %v", err)
	}
}
