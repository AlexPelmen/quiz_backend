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

func TestTrackArtists(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	first, second, track := uuid.New(), uuid.New(), uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO artists(id,name) VALUES($1,'First'),($2,'Second')`, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tracks(id,name) VALUES($1,'Song')`, track); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO artist_to_track(track_id,artist_id,is_primary) VALUES($1,$2,true),($1,$3,false)`, track, first, second); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	var artists []domain.Artist
	decodeResult(t, call("GET", "/api/v1/tracks/"+track.String()+"/artists", "", 200), &artists)
	if len(artists) != 2 || artists[0].ID != first || artists[1].ID != second {
		t.Fatalf("исполнители трека: %+v", artists)
	}
	call("GET", "/api/v1/tracks/"+uuid.NewString()+"/artists", "", 404)
}
