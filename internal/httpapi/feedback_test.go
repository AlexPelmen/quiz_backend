package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestQuestionFeedback(t *testing.T) {
	pool, uow := testutil.Database(t)
	for i := range 10 {
		seedHTTPTrack(t, pool, i)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.Router(service.New(uow, logger, ""), logger)
	var pack domain.QuizPack
	decodeResult(t, client(t, api)("POST", "/api/v1/quiz-packs", `{"name":"Пакет","question_count":10,"round_types":["artist_by_track"]}`, 201), &pack)
	question := pack.Rounds[0].Questions[0].ID
	hash, err := auth.HashPassword("password-1234")
	if err != nil {
		t.Fatal(err)
	}
	playerID := uuid.New()
	for _, user := range []struct {
		id          uuid.UUID
		login, role string
	}{{playerID, "feedback-player", "player"}, {uuid.New(), "feedback-admin", "admin"}} {
		if _, err = pool.Exec(t.Context(), `INSERT INTO users(id,name,login,password_hash,role) VALUES($1,$2,$2,$3,$4)`, user.id, user.login, hash, user.role); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler := auth.Handler(api, sessions, false)
	login := func(name string) *http.Cookie {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBufferString(fmt.Sprintf(`{"login":%q,"password":"password-1234"}`, name))))
		if w.Code != 200 || len(w.Result().Cookies()) == 0 {
			t.Fatalf("логин: %d %s", w.Code, w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	player, admin := login("feedback-player"), login("feedback-admin")
	call := func(cookie *http.Cookie, method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: %d вместо %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	base := "/api/v1/questions/" + question.String()
	call(nil, "POST", base+"/feedback", `{"type":"bug","comment":"Ошибка"}`, 401)
	call(player, "POST", base+"/feedback", `{"type":"bug","comment":"  "}`, 400)
	call(player, "POST", base+"/feedback", `{"type":"like","comment":"Текст"}`, 400)
	call(player, "POST", base+"/feedback", fmt.Sprintf(`{"type":"bug","comment":"Ошибка","user_id":%q}`, uuid.New()), 400)
	call(player, "POST", base+"/feedback", `{"type":"bad_audio","comment":"Плохой звук"}`, 204)
	call(player, "PUT", base+"/like", `{"liked":true}`, 204)
	call(player, "PUT", base+"/like", `{"liked":true}`, 204)
	var state struct {
		Liked bool `json:"liked"`
	}
	decodeResult(t, call(player, "GET", base+"/like", "", 200), &state)
	if !state.Liked {
		t.Fatal("лайк не сохранён")
	}
	call(player, "GET", "/api/v1/question-feedback", "", 403)
	var listing struct {
		Items []struct {
			ID      uuid.UUID `json:"id"`
			Reports int       `json:"reports"`
			Unread  int       `json:"unread"`
			Likes   int       `json:"likes"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decodeResult(t, call(admin, "GET", "/api/v1/question-feedback?filter=likes", "", 200), &listing)
	if listing.Total != 1 || listing.Items[0].ID != question || listing.Items[0].Likes != 1 || listing.Items[0].Reports != 1 || listing.Items[0].Unread != 1 {
		t.Fatalf("неверные счётчики: %+v", listing)
	}
	var reports []struct {
		ID     uuid.UUID `json:"id"`
		UserID uuid.UUID `json:"user_id"`
		Viewed bool      `json:"viewed"`
	}
	if err = json.Unmarshal(call(admin, "GET", "/api/v1/question-feedback/"+question.String(), "", 200), &reports); err != nil || len(reports) != 1 || reports[0].UserID != playerID {
		t.Fatalf("автор жалобы: %+v %v", reports, err)
	}
	call(admin, "PATCH", "/api/v1/question-feedback/"+reports[0].ID.String(), `{"viewed":true}`, 204)
	decodeResult(t, call(admin, "GET", "/api/v1/question-feedback?filter=unread", "", 200), &listing)
	if listing.Total != 0 {
		t.Fatal("жалоба осталась непросмотренной")
	}
	call(player, "PUT", base+"/like", `{"liked":false}`, 204)
	decodeResult(t, call(admin, "GET", "/api/v1/question-feedback?filter=likes", "", 200), &listing)
	if listing.Total != 0 {
		t.Fatal("лайк не снят")
	}
}
