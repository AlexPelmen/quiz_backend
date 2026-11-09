// Package testutil предоставляет изолированные PostgreSQL-схемы интеграционным тестам.
package testutil

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/store"
)

// Database создаёт отдельную схему и удаляет только её после теста.
func Database(t *testing.T) (*pgxpool.Pool, *store.UnitOfWork) {
	t.Helper()

	pool := DatabaseUnmigrated(t)
	ctx := t.Context()
	uow := store.New(pool)

	if err := uow.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	if err := uow.Settings.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	return pool, uow
}

// DatabaseUnmigrated создаёт пустую тестовую схему для проверки миграций.
func DatabaseUnmigrated(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("для интеграционного теста требуется TEST_DATABASE_URL")
	}

	ctx := t.Context()

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	schema := "quiz_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer admin.Close()

		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}

	config.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	return pool
}
