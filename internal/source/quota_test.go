package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devkeeper.com/quiz/internal/domain"
)

func TestGeniusQuotaObservedFromResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "10000")
		w.Header().Set("X-RateLimit-Remaining", "9876")
		w.Header().Set("X-RateLimit-Reset", "3600")
		respondFixture(t, w, `{"response":{}}`)
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.GeniusURL = server.URL
	g := NewGenius(s, "test")

	var observed domain.GeniusQuota

	g.SetQuotaObserver(func(_ context.Context, q domain.GeniusQuota) error { observed = q; return nil })

	before := time.Now()

	if err := g.RefreshQuota(t.Context()); err != nil {
		t.Fatal(err)
	}

	if observed.Limit != 10000 || observed.Remaining != 9876 || observed.ResetAt.Before(before.Add(time.Hour)) {
		t.Fatalf("неверное наблюдение: %+v", observed)
	}

	if _, ok := quotaFromHeaders(http.Header{}, time.Now()); ok {
		t.Fatal("пустые заголовки приняты за лимит")
	}
}
