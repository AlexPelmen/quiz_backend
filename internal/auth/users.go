package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// UserInput задаёт изменяемые поля учётной записи.
type UserInput struct {
	Name        string   `json:"name"`
	Login       string   `json:"login"`
	Password    string   `json:"password"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

func dbError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	return err
}

// List возвращает активные учётные записи.
func (s *Service) List(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE deleted_at IS NULL ORDER BY lower(login),id`)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	users := []User{}

	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}

		users = append(users, u)
	}

	return users, rows.Err()
}

func checkDelegation(actor User, current *User, in UserInput) error {
	if !actor.Can("users") {
		return ErrForbidden
	}

	if actor.Role == "admin" {
		return nil
	}

	if in.Role == "admin" || current != nil && (current.Role == "admin" || current.ID == actor.ID) {
		return ErrForbidden
	}

	if current != nil {
		if current.Role == "moderator" && !actor.Can("album-guessing") {
			return ErrForbidden
		}

		for _, permission := range current.Permissions {
			if !actor.Can(permission) {
				return ErrForbidden
			}
		}
	}

	if in.Role == "moderator" && !actor.Can("album-guessing") {
		return ErrForbidden
	}

	for _, permission := range in.Permissions {
		if !actor.Can(permission) {
			return ErrForbidden
		}
	}

	return nil
}

// Create создаёт учётную запись с назначенными правами.
func (s *Service) Create(ctx context.Context, actor User, in UserInput) (User, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Login = strings.TrimSpace(in.Login)

	if err := validateUser(in.Name, in.Login, in.Role, in.Permissions); err != nil {
		return User{}, err
	}

	if err := checkDelegation(actor, nil, in); err != nil {
		return User{}, err
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	u, err := scanUser(s.pool.QueryRow(ctx, `INSERT INTO users(id,name,login,password_hash,role,permissions) VALUES($1,$2,$3,$4,$5,COALESCE($6::text[],'{}')) RETURNING `+userColumns, uuid.New(), in.Name, in.Login, hash, in.Role, in.Permissions))

	return u, dbError(err)
}

// Update изменяет пользователя и отзывает его сессии при смене прав или пароля.
func (s *Service) Update(ctx context.Context, actor User, id uuid.UUID, in UserInput) (User, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Login = strings.TrimSpace(in.Login)

	if err := validateUser(in.Name, in.Login, in.Role, in.Permissions); err != nil {
		return User{}, err
	}

	var hash string

	if in.Password != "" {
		var err error

		hash, err = HashPassword(in.Password)
		if err != nil {
			return User{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}

	defer rollback(tx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(781238993)`); err != nil {
		return User{}, err
	}

	old, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
	if err != nil {
		return User{}, dbError(err)
	}

	if err = checkDelegation(actor, &old, in); err != nil {
		return User{}, err
	}

	if old.Role == "admin" && in.Role != "admin" {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND deleted_at IS NULL`).Scan(&n); err != nil {
			return User{}, err
		}

		if n <= 1 {
			return User{}, ErrConflict
		}
	}

	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET name=$2,login=$3,role=$4,permissions=COALESCE($5::text[],'{}'),password_hash=COALESCE(NULLIF($6,''),password_hash),updated_at=now() WHERE id=$1 RETURNING `+userColumns, id, in.Name, in.Login, in.Role, in.Permissions, hash))
	if err != nil {
		return User{}, dbError(err)
	}

	if old.Login != in.Login || old.Role != in.Role || !samePermissions(old.Permissions, in.Permissions) || hash != "" {
		if _, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err != nil {
			return User{}, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return User{}, err
	}

	return u, nil
}

func samePermissions(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}

	for _, v := range b {
		if !seen[v] {
			return false
		}
	}

	return true
}

// Delete мягко удаляет учётную запись и отзывает её сессии.
func (s *Service) Delete(ctx context.Context, actor User, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer rollback(tx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(781238993)`); err != nil {
		return err
	}

	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
	if err != nil {
		return dbError(err)
	}

	if err = checkDelegation(actor, &u, UserInput{Role: u.Role}); err != nil {
		return err
	}

	if u.Role == "admin" {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND deleted_at IS NULL`).Scan(&n); err != nil {
			return err
		}

		if n <= 1 {
			return ErrConflict
		}
	}

	if _, err = tx.Exec(ctx, `UPDATE users SET deleted_at=now(),updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Revoke отзывает все сессии пользователя.
func (s *Service) Revoke(ctx context.Context, actor User, id uuid.UUID) error {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 AND deleted_at IS NULL`, id))
	if err != nil {
		return dbError(err)
	}

	if err = checkDelegation(actor, &u, UserInput{Role: u.Role}); err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)

	return err
}

// Profile изменяет имя и при необходимости пароль текущего пользователя.
func (s *Service) Profile(ctx context.Context, id uuid.UUID, name, oldPassword, newPassword string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 1000 {
		return User{}, ErrInvalid
	}

	var hash string

	var err error
	if newPassword != "" {
		hash, err = HashPassword(newPassword)
		if err != nil {
			return User{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}

	defer rollback(tx)

	var current string
	if err = tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&current); err != nil {
		return User{}, dbError(err)
	}

	if hash != "" && !VerifyPassword(current, oldPassword) {
		return User{}, fmt.Errorf("%w: неверный текущий пароль", ErrInvalid)
	}

	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET name=$2,password_hash=COALESCE(NULLIF($3,''),password_hash),updated_at=now() WHERE id=$1 RETURNING `+userColumns, id, name, hash))
	if err != nil {
		return User{}, err
	}

	if hash != "" {
		if _, err = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err != nil {
			return User{}, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return User{}, err
	}

	return u, nil
}
