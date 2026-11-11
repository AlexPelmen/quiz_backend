package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const cookieName = "quiz_session"

// Handler защищает API проверкой сессии и прав по разделам.
func Handler(next http.Handler, service *Service, secureCookie bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			http.NotFound(w, r)
			return
		}

		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "недопустимый источник запроса")
			return
		}

		if r.URL.Path == "/api/v1/auth/login" && r.Method == "POST" {
			loginHTTP(w, r, service, secureCookie)
			return
		}

		cookie, err := r.Cookie(cookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "требуется вход")
			return
		}

		u, err := service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, ErrCredentials) {
				writeError(w, http.StatusUnauthorized, "сессия недействительна")
			} else {
				slog.Error("ошибка проверки сессии", "error", err)
				writeError(w, http.StatusInternalServerError, "ошибка авторизации")
			}

			return
		}

		switch r.URL.Path {
		case "/api/v1/auth/me":
			if r.Method == "GET" {
				writeJSON(w, http.StatusOK, u)
				return
			}
		case "/api/v1/auth/logout":
			if r.Method == "POST" {
				if err := service.Logout(r.Context(), cookie.Value); err != nil {
					writeError(w, 500, "ошибка выхода")
					return
				}

				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureCookie, SameSite: http.SameSiteStrictMode})
				w.WriteHeader(http.StatusNoContent)

				return
			}
		case "/api/v1/auth/profile":
			if r.Method == "PATCH" {
				var f struct {
					Name        string `json:"name"`
					OldPassword string `json:"old_password"`
					NewPassword string `json:"new_password"`
				}

				if !decodeJSON(w, r, &f) {
					return
				}

				updated, err := service.Profile(r.Context(), u.ID, f.Name, f.OldPassword, f.NewPassword)
				if err != nil {
					authError(w, err)
					return
				}

				if f.NewPassword != "" {
					http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureCookie, SameSite: http.SameSiteStrictMode})
				}

				writeJSON(w, 200, updated)

				return
			}
		}

		if strings.HasPrefix(r.URL.Path, "/api/v1/users") {
			usersHTTP(w, r, service, u)
			return
		}

		section := requiredSection(r.Method, strings.TrimPrefix(r.URL.Path, "/api/v1/"))
		if section == "" || !allowedSection(u, section, r) {
			writeError(w, http.StatusForbidden, "недостаточно прав")
			return
		}

		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
		if r.Method == "GET" && len(parts) == 3 && parts[0] == "downloads" && parts[2] == "file" {
			id, err := uuid.Parse(parts[1])
			if err != nil {
				writeError(w, 400, "некорректный ID")
				return
			}

			allowed, err := service.CanReadFile(r.Context(), u, id)
			if err != nil {
				slog.Error("проверка доступа к файлу", "error", err)
				writeError(w, 500, "ошибка проверки доступа")

				return
			}

			if !allowed {
				writeError(w, 403, "недостаточно прав")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}

func requiredSection(method, path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}

	if method == "GET" && (parts[0] == "quiz-packs" || parts[0] == "downloads" && len(parts) == 3 && parts[2] == "file" || parts[0] == "artists" && len(parts) == 2 || parts[0] == "albums" && len(parts) == 2 || parts[0] == "tracks" && (len(parts) == 2 || len(parts) == 3 && parts[2] == "artists")) {
		return "player"
	}

	if method == "POST" && parts[0] == "quiz-rounds" && len(parts) == 3 && parts[2] == "results" {
		return "player"
	}

	if parts[0] == "album-guessing" || parts[0] == "albums" && len(parts) == 3 && (parts[2] == "guessing" || parts[2] == "guessing-preview") {
		return "album-guessing"
	}

	switch parts[0] {
	case "artists", "albums", "tracks", "downloads", "storage", "tags", "settings":
		return parts[0]
	case "quiz-packs":
		return "packs"
	case "parsing-runs":
		return "parsing"
	case "album-cleanup":
		return "album-cleanup"
	case "genius-quota":
		return "parsing"
	case "parsing-errors":
		if method == "POST" && len(parts) == 3 && parts[2] == "requeue" {
			return "parsing-errors"
		}

		return ""
	default:
		return ""
	}
}

func allowedSection(u User, section string, r *http.Request) bool {
	if u.Can(section) {
		return true
	}

	if r.Method != "GET" {
		return false
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	if section == "parsing" && strings.HasPrefix(path, "parsing-runs") && u.Can("parsing-logs") {
		return true
	}

	if section == "artists" && path == "artists" && u.Can("parsing-errors") {
		switch r.URL.Query().Get("status") {
		case "error", "errored", "partial", "manual":
			return true
		}
	}

	if section == "tags" && (u.Can("artists") || u.Can("albums") || u.Can("tracks") || u.Can("packs")) {
		return true
	}

	if section == "artists" && path == "artists" && (u.Can("albums") || u.Can("tracks")) {
		return true
	}

	if section == "albums" && path == "albums" && u.Can("tracks") {
		return true
	}

	if section == "downloads" && path == "downloads" && r.URL.Query().Get("track_id") != "" && u.Can("tracks") {
		return true
	}

	if section == "downloads" && strings.Count(path, "/") == 1 && (u.Can("artists") || u.Can("albums") || u.Can("tracks")) {
		return true
	}

	return false
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()

	if err := d.Decode(target); err != nil {
		writeError(w, 400, "некорректный запрос")
		return false
	}

	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		slog.Error("кодирование ответа авторизации", "error", err)
		http.Error(w, "ошибка операции", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if _, err := w.Write(append(data, '\n')); err != nil {
		slog.Error("запись ответа авторизации", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func authError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCredentials):
		writeError(w, 401, "неверные учётные данные")
	case errors.Is(err, ErrForbidden):
		writeError(w, 403, "недостаточно прав")
	case errors.Is(err, ErrConflict):
		writeError(w, 409, "конфликт: логин занят или это последний администратор")
	case errors.Is(err, ErrNotFound):
		writeError(w, 404, "пользователь не найден")
	case errors.Is(err, ErrInvalid):
		writeError(w, 400, err.Error())
	default:
		slog.Error("ошибка операции авторизации", "error", err)
		writeError(w, 500, "ошибка операции")
	}
}

func loginHTTP(w http.ResponseWriter, r *http.Request, s *Service, secure bool) {
	var f struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}

	if !decodeJSON(w, r, &f) {
		return
	}

	if len(f.Login) > 200 || len(f.Password) > 72 {
		writeError(w, http.StatusUnauthorized, "неверные учётные данные")
		return
	}

	key := strings.ToLower(strings.TrimSpace(f.Login))
	if !s.allowLogin(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "слишком много попыток входа, повторите позже")

		return
	}

	token, u, err := s.Login(r.Context(), f.Login, f.Password)
	if err != nil {
		authError(w, err)
		return
	}

	s.clearLogin(key)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int((24 * time.Hour).Seconds())})
	writeJSON(w, 200, u)
}

func usersHTTP(w http.ResponseWriter, r *http.Request, s *Service, u User) {
	if !u.Can("users") {
		writeError(w, 403, "недостаточно прав")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/users")
	if rest == "" || rest == "/" {
		switch r.Method {
		case "GET":
			result, err := s.List(r.Context())
			if err != nil {
				authError(w, err)
				return
			}

			writeJSON(w, 200, result)

			return
		case "POST":
			var f UserInput
			if !decodeJSON(w, r, &f) {
				return
			}

			result, err := s.Create(r.Context(), u, f)
			if err != nil {
				authError(w, err)
				return
			}

			writeJSON(w, 201, result)

			return
		}
	}

	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")

	id, err := uuid.Parse(parts[0])
	if err != nil || id == uuid.Nil {
		writeError(w, 400, "некорректный ID")
		return
	}

	if len(parts) == 2 && parts[1] == "revoke" && r.Method == "POST" {
		if err := s.Revoke(r.Context(), u, id); err != nil {
			authError(w, err)
			return
		}

		w.WriteHeader(204)

		return
	}

	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case "PUT":
		var f UserInput
		if !decodeJSON(w, r, &f) {
			return
		}

		result, err := s.Update(r.Context(), u, id, f)
		if err != nil {
			authError(w, err)
			return
		}

		writeJSON(w, 200, result)
	case "DELETE":
		if err := s.Delete(r.Context(), u, id); err != nil {
			authError(w, err)
			return
		}

		w.WriteHeader(204)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
