package main

import (
	"net/url"
	"os"
	"testing"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/testutil"
)

func TestCreateOnlyFirstAdministrator(t *testing.T) {
	pool := testutil.DatabaseUnmigrated(t)

	var schema string

	if err := pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}

	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}

	query := dsn.Query()
	query.Set("search_path", schema)

	dsn.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", dsn.String())

	if err := run("admin", "strong-password", "Administrator"); err != nil {
		t.Fatal(err)
	}

	var name, role, hash string
	if err := pool.QueryRow(t.Context(), `SELECT name,role,password_hash FROM users WHERE login='admin'`).Scan(&name, &role, &hash); err != nil {
		t.Fatal(err)
	}

	if name != "Administrator" || role != "admin" || !auth.VerifyPassword(hash, "strong-password") {
		t.Fatal("администратор сохранён неверно")
	}

	if err := run("second", "strong-password", "Second"); err == nil {
		t.Fatal("разрешено создание второго начального администратора")
	}
}
