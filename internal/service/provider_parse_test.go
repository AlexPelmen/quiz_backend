package service

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/testutil"
)

func TestProviderKeepsArtistIDAfterCardFailure(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	id := uuid.New()
	subs := int64(500_000)
	_, err := uow.Artists.Create(ctx, domain.Artist{ID: id, Name: "Example", FullName: "Сохранённое имя", YtSubs: &subs})
	if err != nil {
		t.Fatal(err)
	}
	var searches atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artist":
			searches.Add(1)
			fmt.Fprint(w, `{"artists":[{"id":"known","name":"Example"}]}`)
		case "/artist/known":
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"id":"known","name":"Example","country":"GB","type":"Person"}`)
		case "/release-group":
			fmt.Fprint(w, `{"release-groups":[{"id":"one","title":"One","primary-type":"Album"},{"id":"two","title":"Two","primary-type":"Album"},{"id":"three","title":"Three","primary-type":"Album"}]}`)
		case "/recording":
			fmt.Fprint(w, `{"recordings":[{"id":"song","title":"Song","length":50000}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	settings := domain.DefaultSettings()
	settings.MusicBrainzURL, settings.CoverArtArchiveURL = server.URL, server.URL
	settings.Source429Retries = 0
	run, err := app.StartProviderParsing(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.parseNext(ctx, settings); err != nil {
		t.Fatal(err)
	}
	first, err := uow.Artists.Get(ctx, id)
	if err != nil || first.MusicBrainzID != "known" || first.Status != "error" {
		t.Fatalf("карточка после ошибки: %+v, %v", first, err)
	}
	saved, err := uow.Runs.Get(ctx, run.ID)
	if err != nil || saved.Errors != 1 || saved.Fields == 0 || saved.Albums != 2 || saved.Tracks != 1 {
		t.Fatalf("итог первого прогона: %+v, %v", saved, err)
	}

	if _, err = pool.Exec(ctx, `WITH logo AS (
		INSERT INTO downloads(id,source_type,source_uri,file_type) VALUES($2,'image','https://example.com/logo','image') RETURNING id
	) UPDATE artists SET yt_plays=0,logo_download_id=(SELECT id FROM logo) WHERE id=$1`, id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	fail.Store(false)
	run, err = app.StartProviderParsing(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.parseNext(ctx, settings); err != nil {
		t.Fatal(err)
	}
	result, err := uow.Artists.Get(ctx, id)
	if err != nil || result.Status != "parsed" || result.Country != "GB" || result.FullName != "Сохранённое имя" || searches.Load() != 1 {
		t.Fatalf("повторный импорт: %+v, поисков %d, %v", result, searches.Load(), err)
	}
	saved, err = uow.Runs.Get(ctx, run.ID)
	if err != nil || saved.Errors != 0 || saved.Albums != 0 || saved.Tracks != 0 {
		t.Fatalf("итог повторного прогона: %+v, %v", saved, err)
	}
}

func TestProviderStopsAfterMatchingFailuresOnDifferentArtists(t *testing.T) {
	_, uow := testutil.Database(t)
	ctx := t.Context()
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	for _, name := range []string{"Первый", "Второй", "Третий"} {
		if _, err := uow.Artists.Create(ctx, domain.Artist{ID: uuid.New(), Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	settings := domain.DefaultSettings()
	settings.MusicBrainzURL = server.URL
	settings.Source429Retries = 0
	settings.ParserStopFailures = 2
	run, err := app.StartProviderParsing(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.parseNext(ctx, settings); err != nil {
		t.Fatal(err)
	}
	got, err := uow.Runs.Get(ctx, run.ID)
	if err != nil || got.Status != "stopped" || got.Errors != 2 || got.Processed != 2 || got.Error == "" {
		t.Fatalf("прогон не остановился после двух сбоев: %+v, %v", got, err)
	}
}
