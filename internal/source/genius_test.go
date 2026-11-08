package source

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"devkeeper.com/quiz/internal/domain"
)

func TestLiveRainbowCatalog(t *testing.T) {
	token := os.Getenv("LIVE_GENIUS_ACCESS_TOKEN")
	if token == "" {
		t.Skip("LIVE_GENIUS_ACCESS_TOKEN не задан")
	}

	g := NewGenius(domain.DefaultSettings(), token)
	catalog, err := g.Catalog(t.Context(), Lookup{Name: "Rainbow", TrackLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ArtistID != 35787 || len(catalog.Tracks) == 0 {
		t.Fatalf("неверный каталог Rainbow: ID=%d, треков=%d", catalog.ArtistID, len(catalog.Tracks))
	}
}

func TestNormalizeArtistNames(t *testing.T) {
	for _, names := range [][2]string{
		{"Кино", "КИНО (KINO)"},
		{"Король и Шут", "Король и Шут (Korol I Shut)"},
		{"Guns N' Roses", "Guns N’ Roses"},
		{"Motorhead", "Motörhead"},
		{"Maneskin", "Måneskin"},
		{"H.E.A.T", "HEAT"},
		{"Queen (Tribute)", "Queen"},
	} {
		if normalizeArtistName(names[0]) != normalizeArtistName(names[1]) {
			t.Fatalf("варианты не совпали: %q и %q", names[0], names[1])
		}
	}
}

func TestArtistSearchCompactFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "trustcompany" {
			respondFixture(t, w, `{"response":{"hits":[{"result":{"primary_artist":{"id":322077,"name":"TRUSTcompany"}}}]}}`)
			return
		}
		respondFixture(t, w, `{"response":{"hits":[]}}`)
	}))
	defer server.Close()
	s := domain.DefaultSettings()
	s.GeniusURL = server.URL
	g := NewGenius(s, "test")
	g.api.interval = 0
	id, err := g.artistID(t.Context(), Lookup{Name: "Trust Company"})
	if err != nil || id != 322077 {
		t.Fatalf("ID=%d, ошибка: %v", id, err)
	}
}

func TestArtistSearchRejectsAmbiguousParentheticalNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondFixture(t, w, `{"response":{"hits":[{"result":{"primary_artist":{"id":1,"name":"Dope (Band)"}}},{"result":{"primary_artist":{"id":2,"name":"Dope (Rapper)"}}}]}}`)
	}))
	defer server.Close()
	s := domain.DefaultSettings()
	s.GeniusURL = server.URL
	g := NewGenius(s, "test")
	if _, err := g.artistID(t.Context(), Lookup{Name: "Dope"}); err == nil || !strings.Contains(err.Error(), "несколько исполнителей") {
		t.Fatal("одноимённые исполнители должны оставаться неоднозначными")
	}
}

func TestArtistNameMatches(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        bool
	}{
		{"Король и Шут (Korol I Shut)", "король И шут", true},
		{"Korol I Shut", "Король и Шут", true},
		{"Группа Алиса", "алиса", true},
		{"Алиса Мон", "Алиса", true},
		{"Malisa", "Alisa", false},
		{"Алиса", "", false},
	} {
		if got := artistNameMatches(tc.name, tc.query); got != tc.want {
			t.Errorf("artistNameMatches(%q, %q) = %v, ожидалось %v", tc.name, tc.query, got, tc.want)
		}
	}
}

func TestLiveKorolIShutCatalog(t *testing.T) {
	token := os.Getenv("LIVE_GENIUS_ACCESS_TOKEN")
	if token == "" {
		t.Skip("LIVE_GENIUS_ACCESS_TOKEN не задан")
	}

	g := NewGenius(domain.DefaultSettings(), token)
	catalog, err := g.Catalog(t.Context(), Lookup{Name: "Король и Шут", TrackLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ArtistID != 1381291 || len(catalog.Tracks) == 0 {
		t.Fatalf("неверный каталог Короля и Шута: ID=%d, треков=%d", catalog.ArtistID, len(catalog.Tracks))
	}
}

func TestAmbiguousNormalizedArtistIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondFixture(t, w, `{"response":{"hits":[
			{"result":{"primary_artist":{"id":1,"name":"Motörhead"}}},
			{"result":{"primary_artist":{"id":2,"name":"Motòrhead"}}}]}}`)
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.GeniusURL = server.URL

	g := NewGenius(s, "test")
	if _, err := g.artistID(t.Context(), Lookup{Name: "Motorhead"}); err == nil {
		t.Fatal("неоднозначный вариант не должен выбираться автоматически")
	}
}

func TestArtistSearchAcrossPages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		secondID  int
		wantError bool
	}{
		{name: "artist on third page", secondID: 35787},
		{name: "ambiguous artists on different pages", secondID: 42, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := r.URL.Query().Get("page")
				if page == "1" || page == "2" {
					hit := `{"result":{"primary_artist":{"id":1,"name":"Other"}}}`
					if page == "2" && tc.wantError {
						hit = `{"result":{"primary_artist":{"id":35787,"name":"Rainbow"}}}`
					}
					respondFixture(t, w, `{"response":{"hits":[`+strings.TrimSuffix(strings.Repeat(hit+",", 10), ",")+`]}}`)
					return
				}
				respondFixture(t, w, fmt.Sprintf(`{"response":{"hits":[{"result":{"primary_artist":{"id":%d,"name":"Rainbow"}}}]}}`, tc.secondID))
			}))
			defer server.Close()

			s := domain.DefaultSettings()
			s.GeniusURL = server.URL
			g := NewGenius(s, "test")
			g.api.interval = 0
			id, err := g.artistID(t.Context(), Lookup{Name: "Rainbow"})
			if tc.wantError && err == nil {
				t.Fatal("неоднозначный результат должен возвращать ошибку")
			}
			if !tc.wantError && (err != nil || id != 35787) {
				t.Fatalf("ожидался Rainbow 35787, получено %d, ошибка: %v", id, err)
			}
		})
	}
}

func TestArtistSearchPrefersFullMatch(t *testing.T) {
	for _, tc := range []struct {
		query, hits string
		id          int64
	}{
		{"Алиса", `{"result":{"primary_artist":{"id":1364975,"name":"АлисА (AlisA) (Group)"}}},{"result":{"primary_artist":{"id":2,"name":"Алиса Фрейндлих (Alisa Freindlich)"}}}`, 1364975},
		{"Земфира", `{"result":{"primary_artist":{"id":1025364,"name":"Земфира (Zemfira)"}}},{"result":{"primary_artist":{"id":2,"name":"Земфира от Луки (Zemfira ot Luki)"}}}`, 1025364},
	} {
		t.Run(tc.query, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				respondFixture(t, w, `{"response":{"hits":[`+tc.hits+`]}}`)
			}))
			defer server.Close()
			s := domain.DefaultSettings()
			s.GeniusURL = server.URL
			g := NewGenius(s, "test")
			id, err := g.artistID(t.Context(), Lookup{Name: tc.query})
			if err != nil || id != tc.id {
				t.Fatalf("получен ID %d, ошибка: %v", id, err)
			}
		})
	}
}

func TestArtistSearchFallsBackToTransliteration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "nautilus pompilius" {
			respondFixture(t, w, `{"response":{"hits":[{"result":{"primary_artist":{"id":1108391,"name":"Nautilus Pompilius"}}}]}}`)
			return
		}
		respondFixture(t, w, `{"response":{"hits":[]}}`)
	}))
	defer server.Close()
	s := domain.DefaultSettings()
	s.GeniusURL = server.URL
	g := NewGenius(s, "test")
	g.api.interval = 0
	id, err := g.artistID(t.Context(), Lookup{Name: "Наутилус Помпилиус"})
	if err != nil || id != 1108391 {
		t.Fatalf("не найден Nautilus Pompilius: ID %d, ошибка: %v", id, err)
	}
}

func TestLiveRemainingArtists(t *testing.T) {
	token := os.Getenv("LIVE_GENIUS_ACCESS_TOKEN")
	if token == "" {
		t.Skip("LIVE_GENIUS_ACCESS_TOKEN не задан")
	}
	for _, tc := range []struct {
		name string
		id   int64
	}{{"Алиса", 1364975}, {"Земфира", 1025364}, {"Наутилус Помпилиус", 1108391}} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGenius(domain.DefaultSettings(), token)
			catalog, err := g.Catalog(t.Context(), Lookup{Name: tc.name, TrackLimit: 1})
			if err != nil || catalog.ArtistID != tc.id || len(catalog.Tracks) == 0 {
				t.Fatalf("каталог: ID=%d, треков=%d, ошибка: %v", catalog.ArtistID, len(catalog.Tracks), err)
			}
		})
	}
}

func TestGeniusCatalog(t *testing.T) {
	responses := map[string]string{
		"/official/search":      `{"response":{"hits":[{"result":{"primary_artist":{"id":1,"name":"Группа"}}}]}}`,
		"/official/artists/1":   `{"response":{"artist":{"id":1,"name":"Группа","image_url":""}}}`,
		"/web/artists/1/albums": `{"response":{"albums":[{"id":2,"name":"Альбом"}],"next_page":null}}`,
		"/web/albums/2":         `{"response":{"album":{"id":2,"name":"Альбом","release_date":"2001"}}}`,
		"/web/albums/2/tracks": `{"response":{"tracks":[
			{"number":1,"song":{"id":3,"title":"Первый"}},
			{"number":2,"song":{"id":4,"title":"Второй, с запятой"}}],"next_page":null}}`,
		"/official/artists/1/songs": `{"response":{"songs":[{"id":3},{"id":4}],"next_page":null}}`,
		"/official/songs/3":         `{"response":{"song":{"id":3,"title":"Первый","album":{"id":2},"media":[]}}}`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/official/") && r.Header.Get("Authorization") != "Bearer test" {
			t.Error("секрет не передан официальному API")
		}

		if strings.HasPrefix(r.URL.Path, "/web/") && r.Header.Get("Authorization") != "" {
			t.Error("секрет передан веб-API")
		}

		if r.URL.Path == "/official/artists/1/songs" && r.URL.Query().Get("sort") != "popularity" {
			t.Error("не запрошена сортировка по популярности")
		}

		respondFixture(t, w, responses[r.URL.Path])
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.GeniusURL, s.GeniusWebURL = server.URL+"/official", server.URL+"/web"
	g := NewGenius(s, "test")
	g.api.interval, g.web.interval = 0, 0

	c, err := g.Catalog(t.Context(), Lookup{Name: "Группа", TrackLimit: 1})
	if err != nil {
		t.Fatal(err)
	}

	if len(c.Tracks) != 1 || len(c.Albums) != 1 || !c.Albums[0].Complete || len(c.Tracks[0].Media) != 0 {
		t.Fatalf("каталог или ограничение треков неверны: %+v", c)
	}

	rows, err := csv.NewReader(strings.NewReader(c.Albums[0].TrackList)).ReadAll()
	if err != nil || len(rows) != 3 || rows[2][1] != "Второй, с запятой" {
		t.Fatalf("CSV не содержит полный список: %+v, %v", rows, err)
	}
}

func TestCatalogFallbackWhenWebForbidden(t *testing.T) {
	responses := map[string]string{
		"/artists/1":       `{"response":{"artist":{"name":"Группа"}}}`,
		"/artists/1/songs": `{"response":{"songs":[{"id":3}],"next_page":null}}`,
		"/songs/3":         `{"response":{"song":{"id":3,"title":"Песня","album":{"id":2,"name":"Альбом"}}}}`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/web/") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		respondFixture(t, w, responses[r.URL.Path])
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.GeniusURL, s.GeniusWebURL = server.URL, server.URL+"/web"
	g := NewGenius(s, "test")
	g.api.interval, g.web.interval = 0, 0
	id := int64(1)

	c, err := g.Catalog(t.Context(), Lookup{GeniusID: &id, TrackLimit: 1})
	if err != nil || len(c.Tracks) != 1 || len(c.Albums) != 1 || c.Albums[0].Complete || len(c.Warnings) == 0 {
		t.Fatalf("не сохранены частичные данные: %+v, %v", c, err)
	}
}

func respondFixture(t *testing.T, w http.ResponseWriter, value string) {
	t.Helper()

	if value == "" {
		http.Error(w, "неизвестный запрос", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if _, err := fmt.Fprint(w, value); err != nil {
		t.Error(err)
	}
}

func TestMusicBrainzEnrichment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("не указан User-Agent")
		}

		if r.URL.Path == "/artist" {
			respondFixture(t, w, `{"artists":[{"id":"a","name":"Группа"}]}`)
			return
		}

		respondFixture(t, w, `{"id":"a","name":"Группа","country":"RU","relations":[
			{"type":"member of band","direction":"backward","artist":{"name":"Участник"}}]}`)
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.MusicBrainzURL = server.URL
	m := NewMusicBrainz(s)
	m.api.interval = 0

	result, err := m.Enrich(t.Context(), "Группа")
	if err != nil || result.Country != "RU" || len(result.Members) != 1 {
		t.Fatalf("неверное дополнение: %+v, %v", result, err)
	}

	if _, err := m.Enrich(t.Context(), "Неизвестный"); err == nil {
		t.Fatal("неоднозначное имя не должно подставляться автоматически")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := m.Enrich(ctx, "Группа"); err == nil {
		t.Fatal("отмена не остановила API-запрос")
	}
}
