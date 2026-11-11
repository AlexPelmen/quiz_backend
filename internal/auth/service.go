package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User описывает учётную запись без пароля и токенов.
type User struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Login       string    `json:"login"`
	Role        string    `json:"role"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ErrCredentials означает недействительные учётные данные или сессию.
var ErrCredentials = errors.New("неверный логин или пароль")

// ErrForbidden означает недостаточные права.
var ErrForbidden = errors.New("недостаточно прав")

// ErrInvalid означает некорректные данные пользователя.
var ErrInvalid = errors.New("некорректные данные")

// ErrConflict означает конфликт учётных записей.
var ErrConflict = errors.New("конфликт учётных записей")

// ErrNotFound означает, что пользователь не найден.
var ErrNotFound = errors.New("пользователь не найден")

// Service управляет пользователями и сессиями в PostgreSQL.
type Service struct {
	pool          *pgxpool.Pool
	dummyHash     string
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

type loginAttempt struct {
	count int
	until time.Time
}

func (s *Service) allowLogin(key string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()

	now := time.Now()
	for name, attempt := range s.loginAttempts {
		if now.After(attempt.until) {
			delete(s.loginAttempts, name)
		}
	}

	if len(s.loginAttempts) >= 10000 {
		return false
	}

	attempt := s.loginAttempts[key]
	if now.After(attempt.until) {
		attempt = loginAttempt{until: now.Add(time.Minute)}
	}

	if attempt.count >= 10 {
		return false
	}

	attempt.count++
	s.loginAttempts[key] = attempt

	return true
}

func (s *Service) clearLogin(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginAttempts, key)
}

// New создаёт сервис авторизации.
func New(pool *pgxpool.Pool) (*Service, error) {
	hash, err := HashPassword("dummy-password-not-valid")
	if err != nil {
		return nil, err
	}

	return &Service{pool: pool, dummyHash: hash, loginAttempts: map[string]loginAttempt{}}, nil
}

const userColumns = `id,name,login,role,permissions,created_at,updated_at`

func scanUser(row pgx.Row) (User, error) {
	var u User

	err := row.Scan(&u.ID, &u.Name, &u.Login, &u.Role, &u.Permissions, &u.CreatedAt, &u.UpdatedAt)

	return u, err
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Error("откат транзакции авторизации", "error", err)
	}
}

// Login проверяет учётные данные и создаёт случайную сессию на сутки.
func (s *Service) Login(ctx context.Context, login, password string) (string, User, error) {
	login = strings.TrimSpace(login)

	var id uuid.UUID

	var hash string

	err := s.pool.QueryRow(ctx, `SELECT id,password_hash FROM users WHERE lower(login)=lower($1) AND deleted_at IS NULL`, login).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = s.dummyHash
	} else if err != nil {
		return "", User{}, err
	}

	valid := VerifyPassword(hash, password)
	if !valid || id == uuid.Nil {
		return "", User{}, ErrCredentials
	}

	token, tokenHash, err := NewToken()
	if err != nil {
		return "", User{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", User{}, err
	}

	defer rollback(tx)

	var currentHash string

	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", User{}, ErrCredentials
	}

	if err != nil {
		return "", User{}, err
	}

	if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&currentHash); err != nil {
		return "", User{}, err
	}

	if subtle.ConstantTimeCompare([]byte(currentHash), []byte(hash)) != 1 {
		return "", User{}, ErrCredentials
	}

	if _, err := tx.Exec(ctx, `INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')`, tokenHash[:], id); err != nil {
		return "", User{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", User{}, err
	}

	return token, u, nil
}

// Authenticate проверяет срок действия, отзыв токена и состояние пользователя.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if len(token) != 43 {
		return User{}, ErrCredentials
	}

	hash := TokenHash(token)

	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT u.id,u.name,u.login,u.role,u.permissions,u.created_at,u.updated_at
FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.deleted_at IS NULL`, hash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrCredentials
	}

	return u, err
}

// Logout отзывает текущую сессию.
func (s *Service) Logout(ctx context.Context, token string) error {
	hash := TokenHash(token)
	_, err := s.pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, hash[:])

	return err
}

// Can проверяет доступ пользователя к разделу.
func (u User) Can(section string) bool {
	if u.Role == "admin" || section == "profile" || section == "player" {
		return true
	}

	if u.Role == "moderator" && section == "album-guessing" {
		return true
	}

	for _, permission := range u.Permissions {
		if permission == section {
			return true
		}
	}

	return false
}

// CanReadFile проверяет, что файл нужен доступному разделу или существующему квизу.
func (s *Service) CanReadFile(ctx context.Context, user User, id uuid.UUID) (bool, error) {
	if user.Can("downloads") || user.Can("storage") {
		return true, nil
	}

	var allowed bool

	err := s.pool.QueryRow(ctx, `SELECT
        ($2 AND EXISTS(SELECT 1 FROM artists WHERE logo_download_id=$1)) OR
        ($3 AND EXISTS(SELECT 1 FROM albums WHERE cover_download_id=$1)) OR
        ($4 AND EXISTS(SELECT 1 FROM track_to_download WHERE download_id=$1)) OR
        EXISTS(SELECT 1 FROM quiz_packs WHERE cover_download_id=$1) OR
        EXISTS(SELECT 1 FROM quiz_questions q LEFT JOIN artists a ON a.id=q.artist_id
            LEFT JOIN tracks t ON t.id=q.track_id LEFT JOIN albums al ON al.id=t.album_id OR al.id=q.album_id
            WHERE q.image_download_id=$1 OR q.sound_download_id=$1 OR a.logo_download_id=$1 OR al.cover_download_id=$1)`,
		id, user.Can("artists"), user.Can("albums") || user.Can("album-guessing"), user.Can("tracks")).Scan(&allowed)

	return allowed, err
}

// ValidSections перечисляет поддерживаемые права интерфейса.
var ValidSections = map[string]bool{"artists": true, "albums": true, "tracks": true, "album-guessing": true, "album-cleanup": true, "parsing": true, "parsing-logs": true, "parsing-errors": true, "downloads": true, "storage": true, "packs": true, "tags": true, "settings": true, "console": true, "users": true}

func validateUser(name, login, role string, permissions []string) error {
	if !utf8.ValidString(name) || !utf8.ValidString(login) || strings.TrimSpace(name) == "" || strings.TrimSpace(login) == "" || len(name) > 1000 || len(login) > 200 {
		return fmt.Errorf("%w: укажите имя и логин", ErrInvalid)
	}

	if role != "admin" && role != "moderator" && role != "player" {
		return fmt.Errorf("%w: неизвестная роль", ErrInvalid)
	}

	seen := map[string]bool{}
	for _, p := range permissions {
		if !ValidSections[p] || seen[p] {
			return fmt.Errorf("%w: неизвестное или повторное право", ErrInvalid)
		}

		seen[p] = true
	}

	return nil
}
