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
	"devkeeper.com/quiz/internal/source"
	"devkeeper.com/quiz/internal/testutil"
)

func TestRankedTracksPreserveExistingAndManualCounts(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	artistID := uuid.New()
	subs, oldPlays, manualPlays := int64(270000), int64(2), int64(123)
	artist, err := uow.Artists.Create(ctx, domain.Artist{ID: artistID, Name: "Turnstile", YtSubs: &subs})
	if err != nil {
		t.Fatal(err)
	}
	oldID, otherID, newID := "old", "other", "new"
	for _, track := range []domain.Track{
		{ID: uuid.New(), ArtistID: artistID, Name: "Old", YTMusicID: &oldID, YtPlays: &oldPlays, ManualFields: []string{"yt_plays"}},
		{ID: uuid.New(), ArtistID: artistID, Name: "Other", YTMusicID: &otherID, YtPlays: &oldPlays},
	} {
		if _, err = uow.Tracks.Create(ctx, track); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, "UPDATE artists SET status='processing' WHERE id=$1", artistID); err != nil {
		t.Fatal(err)
	}
	run, err := app.StartProviderParsing(ctx, "yt_music")
	if err != nil {
		t.Fatal(err)
	}
	otherUpdated := int64(9900000)
	p := providerParser{app: app, artist: artist, run: run, rankedTracks: true,
		rankedCandidates: []source.Song{{YTMusicID: &otherID, YtPlays: &otherUpdated}}}
	if err = p.loadRows(ctx); err != nil {
		t.Fatal(err)
	}
	plays := int64(11000000)
	if err = p.save(ctx, source.Catalog{Tracks: []source.Song{
		{Title: "New", YTMusicID: &newID, YtPlays: &plays},
		{Title: "Old", YTMusicID: &oldID, YtPlays: &manualPlays},
	}}, source.Enrichment{}, "tracks_ranked_20"); err != nil {
		t.Fatal(err)
	}
	var count int
	var ids []uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM artist_to_track WHERE artist_id=$1),
		(SELECT track_ids FROM artist_track_shortlists WHERE artist_id=$1)`, artistID).Scan(&count, &ids); err != nil {
		t.Fatal(err)
	}
	if count != 3 || len(ids) != 2 {
		t.Fatalf("старые треки или подборка потеряны: count=%d ids=%v", count, ids)
	}
	var savedManual int64
	if err = pool.QueryRow(ctx, "SELECT yt_plays FROM tracks WHERE yt_music_id=$1", oldID).Scan(&savedManual); err != nil || savedManual != oldPlays {
		t.Fatalf("ручной счётчик изменён: %d, %v", savedManual, err)
	}
	var refreshed int64
	if err = pool.QueryRow(ctx, "SELECT yt_plays FROM tracks WHERE yt_music_id=$1", otherID).Scan(&refreshed); err != nil || refreshed != otherUpdated {
		t.Fatalf("счётчик ранее сохранённого трека не обновлён: %d, %v", refreshed, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE artists SET status='partial' WHERE id=$1", artistID); err != nil {
		t.Fatal(err)
	}
	if err = app.RetryArtist(ctx, artistID); err != nil {
		t.Fatal(err)
	}
	var marker bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM artist_provider_lists WHERE artist_id=$1 AND kind='tracks_ranked_20')`, artistID).Scan(&marker); err != nil || marker {
		t.Fatalf("повторная проверка подборки не разрешена: %v, %v", marker, err)
	}
	if err = pool.QueryRow(ctx, "SELECT track_ids FROM artist_track_shortlists WHERE artist_id=$1", artistID).Scan(&ids); err != nil || len(ids) != 2 {
		t.Fatalf("действующая подборка потеряна до повторного импорта: %v, %v", ids, err)
	}
}

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
