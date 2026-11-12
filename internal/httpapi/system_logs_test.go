package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/logbuffer"
)

func TestSystemLogs(t *testing.T) {
	logs := logbuffer.New(3)
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(io.Discard, logs), nil))
	logger.Info("first")
	logger.Warn("second")
	router := httpapi.Router(nil, logger, logs)
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	w := call("/api/v1/system-logs?limit=1")
	var result struct {
		Entries []struct {
			Message string `json:"msg"`
			Level   string `json:"level"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&result); err != nil || w.Code != 200 || len(result.Entries) != 1 || result.Entries[0].Message != "second" || result.Entries[0].Level != "WARN" {
		t.Fatalf("журнал: %d %s %v", w.Code, w.Body.String(), err)
	}
	if got := call("/api/v1/system-logs?limit=1001").Code; got != 400 {
		t.Fatalf("некорректный limit: %d", got)
	}
}
