// Команда create-admin создаёт первого администратора в базе без пользователей.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/store"
)

func main() {
	login := flag.String("login", "", "логин администратора")
	password := flag.String("password", "", "пароль администратора")
	name := flag.String("name", "", "отображаемое имя; по умолчанию логин")

	flag.Parse()

	if err := run(*login, *password, *name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("Администратор создан")
}

func run(login, password, name string) (result error) {
	login = strings.TrimSpace(login)
	name = strings.TrimSpace(name)

	if login == "" || len(login) > 200 {
		return errors.New("укажите логин длиной до 200 байт")
	}

	if name == "" {
		name = login
	}

	if len(name) > 1000 {
		return errors.New("имя слишком длинное")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("требуется DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}

	defer pool.Close()

	if err := store.New(pool).Migrate(ctx); err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			result = errors.Join(result, err)
		}
	}()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(781238992)`); err != nil {
		return err
	}

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return err
	}

	if exists {
		return errors.New("первого администратора можно создать только в базе без пользователей")
	}

	if _, err := tx.Exec(ctx, `INSERT INTO users(id,name,login,password_hash,role) VALUES($1,$2,$3,$4,'admin')`, uuid.New(), name, login, hash); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
