package service

import (
	"context"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

type lookupRecorder struct {
	lookups []source.Lookup
}

func (r *lookupRecorder) Catalog(_ context.Context, lookup source.Lookup) (source.Catalog, error) {
	r.lookups = append(r.lookups, lookup)
	return source.Catalog{}, nil
}

func TestCatalogCollectUsesGeniusOnlyForGaps(t *testing.T) {
	genius := &lookupRecorder{}

	catalog := source.Catalog{
		Name: "Artist", LogoURL: "https://example.com/artist.jpg",
		Tracks: []source.Song{{Title: "Song"}},
	}
	for _, title := range []string{"One", "Two", "Three", "Four", "Five"} {
		catalog.Albums = append(catalog.Albums, source.Album{
			Name: title, CoverURL: "https://example.com/cover.jpg", ReleaseDate: "2020",
		})
	}

	deps := parseDependencies{
		Settings: domain.DefaultSettings(), Source: genius,
		YouTubeMusic: func(context.Context, domain.Settings, domain.Artist, []source.Song) (source.Catalog, error) {
			return catalog, nil
		},
	}
	app := &App{}
	artist := domain.Artist{Name: "Artist"}
	app.collectCatalog(t.Context(), &artist, deps, source.Catalog{})

	if len(genius.lookups) != 0 {
		t.Fatalf("Genius вызван при готовом каталоге: %+v", genius.lookups)
	}

	catalog.Albums[0].CoverURL = ""

	app.collectCatalog(t.Context(), &artist, deps, source.Catalog{})

	if len(genius.lookups) != 1 || genius.lookups[0].TrackLimit != 0 {
		t.Fatalf("для обложки не требуется список треков: %+v", genius.lookups)
	}
}

func TestStudioAlbumSelectionKeepsRankAndRejectsOtherEditions(t *testing.T) {
	candidates := []source.Album{
		{Name: "One (Deluxe Edition)"},
		{Name: "Concert"},
		{Name: "One"},
		{Name: "one"},
		{Name: "A single", Type: "Single"},
		{Name: "Two"},
		{Name: "Three"},
		{Name: "Four"},
		{Name: "Five"},
		{Name: "Six"},
	}

	albums := pickStudioAlbums(candidates, []source.Album{{Name: "Concert", Type: "Live"}}, 3)
	if len(albums) != 3 || albums[0].Name != "One" || albums[2].Name != "Three" {
		t.Fatalf("неверная подборка: %+v", albums)
	}
}

func TestAlbumPlaysOnlyRequestsSelectedGaps(t *testing.T) {
	first, known, manual, outside := "first", "known", "manual", "outside"
	savedPlays, foundPlays := int64(500), int64(900)
	catalog := source.Catalog{Albums: []source.Album{
		{Name: "First", YTMusicID: &first},
		{Name: "Known", YTMusicID: &known},
		{Name: "Manual", YTMusicID: &manual},
		{Name: "MusicBrainz only"},
		{Name: "Fifth"},
		{Name: "Sixth", YTMusicID: &outside},
	}}
	saved := source.Catalog{Albums: []source.Album{
		{Name: "Known", YTMusicID: &known, YtPlays: &savedPlays},
		{Name: "Manual", YTMusicID: &manual, SkipPlays: true},
	}}
	deps := parseDependencies{
		Settings: domain.DefaultSettings(),
		YouTubeMusic: func(context.Context, domain.Settings, domain.Artist, []source.Song) (source.Catalog, error) {
			return catalog, nil
		},
		AlbumPlays: func(_ context.Context, _ domain.Settings, lookup source.Catalog) (source.Catalog, error) {
			if len(lookup.Albums) != 1 || *lookup.Albums[0].YTMusicID != first {
				t.Fatalf("лишние запросы прослушиваний: %+v", lookup.Albums)
			}

			return source.Catalog{Albums: []source.Album{{YTMusicID: &first, YtPlays: &foundPlays}}}, nil
		},
	}
	subs := int64(5_000_000)
	app, artist := &App{}, domain.Artist{Name: "Artist", YtSubs: &subs}

	got := app.collectCatalog(t.Context(), &artist, deps, saved)
	if len(got.Albums) != 5 || got.Albums[0].YtPlays != &foundPlays || got.Albums[1].YtPlays != &savedPlays ||
		got.Albums[2].YtPlays != nil || len(got.Tracks) != 0 {
		t.Fatalf("метрики и треки после импорта: %+v %+v", got.Albums, got.Tracks)
	}
}
