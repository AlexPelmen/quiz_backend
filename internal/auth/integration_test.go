package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/testutil"
)

func TestSessionsAndSectionPermissions(t *testing.T) {
	pool, _ := testutil.Database(t)
	ctx := t.Context()

	hash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,name,login,password_hash,role) VALUES($1,'Moderator','mod',$2,'moderator')`, id, hash); err != nil {
		t.Fatal(err)
	}

	svc, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}

	h := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), svc, false)

	call := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&b).Encode(body); err != nil {
				t.Fatal(err)
			}
		}

		req := httptest.NewRequest(method, path, &b)
		if cookie != nil {
			req.AddCookie(cookie)
		}

		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)

		return response
	}
	if got := call("GET", "/api/v1/settings", nil, nil).Code; got != 401 {
		t.Fatalf("без сессии: %d", got)
	}

	if got := call("POST", "/api/v1/auth/login", map[string]string{"login": "mod", "password": "wrong"}, nil).Code; got != 401 {
		t.Fatalf("неверный пароль: %d", got)
	}

	login := call("POST", "/api/v1/auth/login", map[string]string{"login": "mod", "password": "test-password"}, nil)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("вход: %d %s", login.Code, login.Body.String())
	}

	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.MaxAge != 86400 || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", cookie)
	}

	if got := call("GET", "/api/v1/album-guessing", nil, cookie).Code; got != 204 {
		t.Fatalf("модерация: %d", got)
	}

	if got := call("PATCH", "/api/v1/settings", nil, cookie).Code; got != 403 {
		t.Fatalf("настройки: %d", got)
	}

	unrelated := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO downloads(id,source_type,file_type,status) VALUES($1,'upload','mp3','finished')`, unrelated); err != nil {
		t.Fatal(err)
	}

	if got := call("GET", "/api/v1/downloads/"+unrelated.String()+"/file", nil, cookie).Code; got != 403 {
		t.Fatalf("чужой файл: %d", got)
	}

	cover, artist, album := uuid.New(), uuid.New(), uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO downloads(id,source_type,file_type,status) VALUES($1,'upload','png','finished')`, cover); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO artists(id,name) VALUES($1,'Cover artist')`, artist); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO albums(id,name,cover_download_id) VALUES($1,'Album',$2)`, album, cover); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO artist_to_album(album_id,artist_id) VALUES($1,$2)`, album, artist); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if got := call("GET", "/api/v1/downloads/"+cover.String()+"/file", nil, cookie).Code; got != 204 {
		t.Fatalf("обложка для модерации: %d", got)
	}

	crossSite := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	crossSite.AddCookie(cookie)
	crossSite.Header.Set("Origin", "https://untrusted.example")

	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, crossSite)

	if blocked.Code != 403 {
		t.Fatalf("межсайтовый запрос: %d", blocked.Code)
	}

	if got := call("GET", "/api/v1/auth/me", nil, cookie).Code; got != 200 {
		t.Fatalf("сессия: %d", got)
	}

	if _, err := pool.Exec(ctx, `UPDATE user_sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}

	if got := call("GET", "/api/v1/auth/me", nil, cookie).Code; got != 401 {
		t.Fatalf("истёкшая сессия: %d", got)
	}
}

func TestUserManagementAndRevocation(t *testing.T) {
	pool, _ := testutil.Database(t)
	ctx := t.Context()

	hash, err := auth.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,name,login,password_hash,role) VALUES($1,'Admin','admin',$2,'admin')`, id, hash); err != nil {
		t.Fatal(err)
	}

	svc, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}

	token, admin, err := svc.Login(ctx, "admin", "admin-password")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, admin, id); err == nil {
		t.Fatal("последнего администратора можно удалить")
	}

	user, err := svc.Create(ctx, admin, auth.UserInput{Name: "Player", Login: "player", Password: "player-password", Role: "player", Permissions: []string{"users"}})
	if err != nil {
		t.Fatal(err)
	}

	playerToken, player, err := svc.Login(ctx, "player", "player-password")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Create(ctx, player, auth.UserInput{Name: "Other", Login: "other", Password: "other-password", Role: "admin"}); err == nil {
		t.Fatal("делегированный пользователь создал администратора")
	}

	if err := svc.Revoke(ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Authenticate(ctx, playerToken); err == nil {
		t.Fatal("отозванная сессия работает")
	}

	if _, err := svc.Profile(ctx, admin.ID, "Renamed", "wrong", "new-password"); err == nil {
		t.Fatal("пароль изменён без старого")
	}

	if _, err := svc.Profile(ctx, admin.ID, "Renamed", "admin-password", "new-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Authenticate(ctx, token); err == nil {
		t.Fatal("сессия после смены пароля работает")
	}

	if _, _, err := svc.Login(ctx, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
}
