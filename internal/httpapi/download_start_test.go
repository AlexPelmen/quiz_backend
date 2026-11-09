package httpapi_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestDownloadSelection(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := service.New(uow, logger, "")
	call := client(t, httpapi.Router(app, logger))

	seedHTTPTrack(t, pool, 1)

	var track uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM tracks").Scan(&track); err != nil {
		t.Fatal(err)
	}

	first, second, image := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{first, second} {
		if _, err := uow.Downloads.Create(ctx, domain.Download{
			ID: id, SourceType: "unknown", FileType: "mp3", Meta: map[string]any{},
		}); err != nil {
			t.Fatal(err)
		}

		if err := uow.Downloads.Link(ctx, track, id); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := uow.Downloads.Create(ctx, domain.Download{
		ID: image, SourceType: "image", FileType: "image",
		SourceURI: "https://example.com/image", Meta: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}

	call("POST", "/api/v1/downloads/start", `{"downloader":"hitmo","download_ids":[]}`, 400)
	call("POST", "/api/v1/downloads/start",
		fmt.Sprintf(`{"downloader":"hitmo","download_ids":[%q,%q]}`, first, image), 409)

	if d, err := uow.Downloads.Get(ctx, first); err != nil || d.Status != "created" {
		t.Fatalf("неатомарный старт: %+v %v", d, err)
	}

	var wg sync.WaitGroup

	results := make(chan error, 2)

	for _, method := range []string{"hitmo", "genius_youtube"} {
		wg.Go(func() {
			_, err := app.StartDownloads(ctx, service.StartDownloadsForm{Downloader: method, DownloadIDs: []uuid.UUID{first}})
			results <- err
		})
	}

	wg.Wait()

	var started, conflicts int

	for range 2 {
		err := <-results

		switch {
		case err == nil:
			started++
		case errors.Is(err, domain.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}

	if started != 1 || conflicts != 1 {
		t.Fatalf("запущено %d, конфликтов %d", started, conflicts)
	}

	if d, err := uow.Downloads.Get(ctx, second); err != nil || d.Status != "created" {
		t.Fatalf("невыбранное задание: %+v %v", d, err)
	}

	call("POST", "/api/v1/downloads/start", fmt.Sprintf(`{"downloader":"image","download_ids":[%q]}`, image), 201)
}
