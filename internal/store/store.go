// Package store выполняет SQL-запросы и управляет транзакциями PostgreSQL.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB — минимальный общий набор операций пула и транзакции.
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// UnitOfWork предоставляет репозитории с общим соединением или транзакцией.
type UnitOfWork struct {
	db        DB
	pool      *pgxpool.Pool
	Artists   ArtistRepository
	Albums    AlbumRepository
	Tracks    TrackRepository
	Downloads DownloadRepository
	Runs      RunRepository
	Packs     PackRepository
	Settings  SettingsRepository
	Quota     QuotaRepository
	Tags      TagRepository
}

// New создаёт Unit of Work поверх пула соединений.
func New(pool *pgxpool.Pool) *UnitOfWork {
	u := bind(pool)
	u.pool = pool

	return u
}

// Ping проверяет доступность PostgreSQL.
func (u *UnitOfWork) Ping(ctx context.Context) error {
	return u.pool.Ping(ctx)
}

func bind(db DB) *UnitOfWork {
	return &UnitOfWork{
		db: db, Artists: ArtistRepository{db}, Albums: AlbumRepository{db},
		Tracks: TrackRepository{db}, Downloads: DownloadRepository{db},
		Runs: RunRepository{db}, Packs: PackRepository{db}, Settings: SettingsRepository{db},
		Quota: QuotaRepository{db}, Tags: TagRepository{db},
	}
}

// Transaction выполняет функцию атомарно; вложенные транзакции не поддерживаются.
func (u *UnitOfWork) Transaction(ctx context.Context, fn func(*UnitOfWork) error) (result error) {
	return u.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, fn)
}

func (u *UnitOfWork) transaction(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(*UnitOfWork) error,
) (result error) {
	if u.pool == nil {
		return errors.New("вложенная транзакция не поддерживается")
	}

	tx, err := u.pool.BeginTx(ctx, options)
	if err != nil {
		return err
	}

	defer func() {
		// Rollback после Commit возвращает ErrTxClosed и не является ошибкой приложения.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		err := tx.Rollback(cleanupCtx)
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			result = errors.Join(result, err)
		}
	}()

	if err := fn(bind(tx)); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Migrate применяет встроенные миграции под блокировкой PostgreSQL.
func (u *UnitOfWork) Migrate(ctx context.Context) error {
	return u.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx *UnitOfWork) error {
		if _, err := tx.db.Exec(ctx, "SELECT pg_advisory_xact_lock(781238991)"); err != nil {
			return err
		}

		if _, err := tx.db.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY)"); err != nil {
			return err
		}

		return tx.migrateFiles(ctx)
	})
}

func (u *UnitOfWork) migrateFiles(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		var exists bool
		if err := u.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)",
			entry.Name()).Scan(&exists); err != nil {
			return err
		}

		if exists {
			continue
		}

		sql, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}

		if _, err := u.db.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("миграция %s: %w", entry.Name(), err)
		}

		if _, err := u.db.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", entry.Name()); err != nil {
			return err
		}
	}

	return nil
}

func query[T any](ctx context.Context, db DB, sql string, args []any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, translate(err)
	}

	r, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if r == nil {
		r = []T{}
	}

	return r, translate(err)
}

func one[T any](ctx context.Context, db DB, sql string, args []any) (T, error) {
	r, err := query[T](ctx, db, sql, args)
	if err != nil {
		return *new(T), err
	}

	if len(r) == 0 {
		return *new(T), domain.ErrNotFound
	}

	return r[0], nil
}

func execute(ctx context.Context, db DB, sql string, args []any) error {
	result, err := db.Exec(ctx, sql, args...)
	if err != nil {
		return translate(err)
	}

	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "40001":
			return fmt.Errorf("%w: %s", domain.ErrConflict, pgErr.ConstraintName)
		case "23514", "23502", "22P02":
			return fmt.Errorf("%w: %s", domain.ErrInvalid, pgErr.ConstraintName)
		}
	}

	return err
}
