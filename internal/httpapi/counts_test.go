package httpapi_test

import (
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
	"io"
	"log/slog"
	"testing"
)

func TestListCounts(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	call("POST", "/api/v1/artists", `{"name":"Alpha"}`, 201)
	call("POST", "/api/v1/artists", `{"name":"Beta"}`, 201)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/artists/count", 2}, {"/api/v1/artists/count?name=alpha", 1}, {"/api/v1/artists/count?status=manual", 0},
		{"/api/v1/albums/count", 0}, {"/api/v1/tracks/count", 0}, {"/api/v1/quiz-packs/count", 0},
		{"/api/v1/album-guessing/count", 0}, {"/api/v1/album-cleanup/count", 0},
	} {
		var result struct {
			Count int `json:"count"`
		}
		decodeResult(t, call("GET", tc.path, "", 200), &result)
		if result.Count != tc.want {
			t.Errorf("%s: получили %d, ожидали %d", tc.path, result.Count, tc.want)
		}
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO downloads(id,source_type,file_type,status,meta) VALUES (gen_random_uuid(),'unknown','mp3','pending','{}'),(gen_random_uuid(),'image','image','processing','{}')`); err != nil {
		t.Fatal(err)
	}
	var active struct {
		Active int `json:"active"`
	}
	decodeResult(t, call("GET", "/api/v1/downloads/active-count?file_type=mp3", "", 200), &active)
	if active.Active != 1 {
		t.Fatalf("активных mp3: %d", active.Active)
	}
	var count struct {
		Count int `json:"count"`
	}
	decodeResult(t, call("GET", "/api/v1/downloads/count?file_type=image", "", 200), &count)
	if count.Count != 1 {
		t.Fatalf("изображений: %d", count.Count)
	}
	call("GET", "/api/v1/downloads/active-count?file_type=bad", "", 400)
}
