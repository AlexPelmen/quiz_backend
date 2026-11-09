package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestAlbumCleanupPreservesSelectedAlbumsAndTracks(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()

	seedHTTPTrack(t, pool, 1)

	var artist, extra uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT artist_id,album_id FROM artist_to_album").Scan(&artist, &extra); err != nil {
		t.Fatal(err)
	}

	kept, err := uow.Albums.Create(ctx, domain.Album{
		ID: uuid.New(), ArtistID: artist, Name: "Студийный альбом",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := uow.Albums.SaveShortlist(ctx, artist, []uuid.UUID{kept.ID}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	var preview []store.AlbumCleanupRow

	decodeResult(t, call("GET", "/api/v1/album-cleanup", "", 200), &preview)

	if len(preview) != 1 || preview[0].ID != extra || preview[0].Protected {
		t.Fatalf("неверный список: %+v", preview)
	}

	call("POST", "/api/v1/album-cleanup", fmt.Sprintf(`{"ids":[%q,%q]}`, extra, kept.ID), 409)
	decodeResult(t, call("GET", "/api/v1/album-cleanup", "", 200), &preview)

	if len(preview) != 1 {
		t.Fatal("частичное удаление после конфликта")
	}

	call("POST", "/api/v1/album-cleanup", fmt.Sprintf(`{"ids":[%q]}`, extra), 204)

	var albums, tracks int

	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM albums),
        (SELECT count(*) FROM tracks WHERE album_id IS NULL)`).Scan(&albums, &tracks)
	if err != nil || albums != 1 || tracks != 1 {
		t.Fatalf("после очистки: альбомов %d, треков без альбома %d, ошибка %v", albums, tracks, err)
	}
}

func TestParsingRefreshAndEvents(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()

	seedHTTPTrack(t, pool, 1)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	var run domain.ParseRun

	decodeResult(t, call("POST", "/api/v1/parsing-runs", `{"refresh":true}`, 202), &run)

	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM artists").Scan(&status); err != nil || status != "created" {
		t.Fatalf("исполнитель не поставлен в очередь: %q %v", status, err)
	}

	for _, stage := range []string{"Исполнитель", "YouTube Music"} {
		if err := uow.Runs.AddEvent(ctx, run.ID, store.ParseEvent{Stage: stage, Message: "Начали обработку"}); err != nil {
			t.Fatal(err)
		}
	}

	var events []store.ParseEvent

	body := call("GET", "/api/v1/parsing-runs/"+run.ID.String()+"/events?limit=1", "", 200)
	if err := json.Unmarshal(body, &events); err != nil || len(events) != 1 || events[0].Stage != "YouTube Music" {
		t.Fatalf("неверный журнал: %+v %v", events, err)
	}
}
