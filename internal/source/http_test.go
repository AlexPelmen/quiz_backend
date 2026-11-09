package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"12", 12 * time.Second},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute},
		{"-3", 0},
		{"invalid", 0},
		{"999999999999999999999", 0},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(tc.value, now); got != tc.want {
			t.Fatalf("%q: получили %v, ожидали %v", tc.value, got, tc.want)
		}
	}
}

func TestRequestResumesAfter503(t *testing.T) {
	calls := make(chan time.Time, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls <- time.Now()
		if len(calls) < 4 {
			w.Header().Set("Retry-After", "invalid")
			http.Error(w, "wait", http.StatusServiceUnavailable)

			return
		}

		respondFixture(t, w, `{"value":"ok"}`)
	}))
	defer server.Close()

	c := jsonClient{
		client: server.Client(), base: server.URL, retryStatus: http.StatusServiceUnavailable, retries: 3,
		retryDelay: 10 * time.Millisecond, maxRetryDelay: 40 * time.Millisecond,
	}

	var result struct{ Value string }

	if err := c.get(t.Context(), "/", &result); err != nil || result.Value != "ok" {
		t.Fatalf("запрос не восстановился: %+v %v", result, err)
	}

	first, second, third, fourth := <-calls, <-calls, <-calls, <-calls
	if second.Sub(first) < 10*time.Millisecond || third.Sub(second) < 20*time.Millisecond ||
		fourth.Sub(third) < 30*time.Millisecond {
		t.Fatal("задержка между повторами не соблюдена")
	}

}

func Test429RetriesRespectBounds(t *testing.T) {
	for _, mode := range []string{"bounded", "retry-after"} {
		t.Run(mode, func(t *testing.T) {
			calls := make(chan struct{}, 4)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls <- struct{}{}

				if mode == "retry-after" {
					w.Header().Set("Retry-After", "3600")
				}

				http.Error(w, "wait", http.StatusTooManyRequests)
			}))
			defer server.Close()

			c := jsonClient{
				client: server.Client(), base: server.URL, retryStatus: http.StatusTooManyRequests, retries: 2,
				retryDelay: time.Millisecond, maxRetryDelay: 2 * time.Millisecond,
			}

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			err := c.get(ctx, "/", &struct{}{})

			var apiErr *HTTPError

			if !errors.As(err, &apiErr) || apiErr.Status != 429 {
				t.Fatalf("потерян код 429: %v", err)
			}

			want := 3
			if mode == "retry-after" {
				want = 1
			}

			if len(calls) != want {
				t.Fatalf("запросов %d, ожидали %d", len(calls), want)
			}
		})
	}
}

func TestForbiddenIsNotRetried(t *testing.T) {
	calls := make(chan struct{}, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls <- struct{}{}

		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	c := jsonClient{client: server.Client(), base: server.URL, retryStatus: http.StatusTooManyRequests, retries: 3}
	if err := c.get(t.Context(), "/", &struct{}{}); err == nil || len(calls) != 1 {
		t.Fatalf("403 не должен повторяться: %v, запросов %d", err, len(calls))
	}
}
