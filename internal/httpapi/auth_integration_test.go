package httpapi_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
	"github.com/google/uuid"
)

func TestProtectedRouterModerator(t *testing.T) {
	pool, uow := testutil.Database(t)
	hash, err := auth.HashPassword("moderator-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users(id,name,login,password_hash,role) VALUES($1,'Moderator','moderator',$2,'moderator')`, uuid.New(), hash); err != nil {
		t.Fatal(err)
	}
	authorization, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := auth.Handler(httpapi.Router(service.New(uow, logger, ""), logger), authorization, false)
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBufferString(`{"login":"moderator","password":"moderator-password"}`)))
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("вход: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/album-guessing", 200},
		{"GET", "/api/v1/quiz-packs", 200},
		{"GET", "/api/v1/settings", 403},
		{"GET", "/api/v1/users", 403},
		{"POST", "/api/v1/artists", 403},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Errorf("%s %s: %d, ожидалось %d", tc.method, tc.path, response.Code, tc.want)
		}
	}
}
