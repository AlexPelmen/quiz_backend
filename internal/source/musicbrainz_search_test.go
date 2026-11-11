package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devkeeper.com/quiz/internal/domain"
)

func TestMusicBrainzArtistSearch(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{
		{"Maneskin", `{"artists":[{"id":"1","name":"Måneskin"}]}`, "1"},
		{"Trust Company", `{"artists":[{"id":"2","name":"TRUSTcompany"}]}`, "2"},
		{"Сектор Газа", `{"artists":[{"id":"3","name":"Sektor Gaza"}]}`, "3"},
		{"Kiss", `{"artists":[{"id":"popular","name":"KISS"},{"id":"other","name":"Kiss"}]}`, "popular"},
		{"Kiss", `{"artists":[{"id":"wrong","name":"Kiss tribute"},{"id":"right","name":"Kiss"}]}`, "right"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.response) }))
			defer server.Close()
			s := domain.DefaultSettings()
			s.MusicBrainzURL = server.URL
			m := NewMusicBrainz(s)
			m.api.interval = 0
			got, err := m.FindArtist(t.Context(), tc.name)
			if err != nil || got != tc.want {
				t.Fatalf("ID=%q, ошибка=%v; ожидается %q", got, err, tc.want)
			}
		})
	}
}

func TestMusicBrainzSearchRetriesCompactName(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		if strings.Contains(query, "trustcompany") {
			_, _ = fmt.Fprint(w, `{"artists":[{"id":"match","name":"TRUSTcompany"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"artists":[]}`)
	}))
	defer server.Close()
	s := domain.DefaultSettings()
	s.MusicBrainzURL = server.URL
	m := NewMusicBrainz(s)
	m.api.interval = 0
	got, err := m.FindArtist(t.Context(), "Trust Company")
	if err != nil || got != "match" || len(queries) != 2 {
		t.Fatalf("ID=%q, запросы=%v, ошибка=%v", got, queries, err)
	}
}
