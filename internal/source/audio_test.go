package source

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"devkeeper.com/quiz/internal/domain"
)

func TestHitmoDurationAndAmbiguity(t *testing.T) {
	const first = "https://rus.hitmos.fm/audio/first.mp3"

	const second = "https://rus.hitmos.fm/audio/second.mp3"

	const response = `{\"download\":\"` + first + `\",\"duration\":\"181\"}`

	tests := []struct {
		name     string
		html     string
		duration int
		wantErr  error
	}{
		{"minus one", response, 182, nil},
		{"plus one", response, 180, nil},
		{"outside", response, 179, domain.ErrNotFound},
		{"ambiguous", response + `{"download":"` + second + `","duration":180}`, 180, domain.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := matchHitmoAudio(test.html, test.duration)
			if !errors.Is(err, test.wantErr) || test.wantErr == nil && got != first {
				t.Fatalf("получено %q, %v", got, err)
			}
		})
	}
}

func TestHitmoForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("q") != "Группа - Трек" {
			t.Errorf("неверный поиск: %s", r.URL)
		}

		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.HitmoURL = server.URL
	_, err := HitmoAudio(t.Context(), s, "Группа - Трек", 180)

	var response *HTTPError

	if !errors.As(err, &response) || response.Status != http.StatusForbidden {
		t.Fatalf("потерян код 403: %v", err)
	}
}

func TestGeniusPreviewFromPlayer(t *testing.T) {
	const clip = "https://audio-ssl.itunes.apple.com/itunes-assets/clip.m4a"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/with-preview" {
			respondFixture(t, w, `{"player":{"previews":[{"url":"`+clip+`"}]}}`)
		} else {
			respondFixture(t, w, `{"player":{}}`)
		}
	}))
	defer server.Close()

	g := NewGenius(domain.DefaultSettings(), "")

	got, err := g.Preview(t.Context(), Song{URL: server.URL + "/with-preview"})
	if got != clip || err != nil {
		t.Fatalf("превью: %q, %v", got, err)
	}

	if _, err := g.Preview(t.Context(), Song{URL: server.URL + "/without-preview"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ожидалась диагностика отсутствующего превью: %v", err)
	}
}
