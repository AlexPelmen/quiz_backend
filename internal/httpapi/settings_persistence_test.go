package httpapi_test

import (
	"io"
	"log/slog"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestGuessRateSettingPersists(t *testing.T) {
	_, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"quiz_guess_rate_enabled":false}`, false},
		{`{"quiz_guess_rate_enabled":true}`, true},
	} {
		call("PATCH", "/api/v1/settings", tc.body, 200)
		var got domain.Settings
		decodeResult(t, call("GET", "/api/v1/settings", "", 200), &got)
		if got.QuizGuessRateEnabled != tc.want || got.QuizGuessRateAskedThreshold != domain.DefaultSettings().QuizGuessRateAskedThreshold {
			t.Fatalf("настройки после повторного чтения: %+v", got)
		}
	}
}
