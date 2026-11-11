// Команда quiz запускает HTTP API и фоновых работников музыкального квиза.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("сервис завершился с ошибкой", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("требуется DATABASE_URL")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}

	defer pool.Close()

	uow := store.New(pool)
	if err := initialize(ctx, uow); err != nil {
		return err
	}

	app := service.New(uow, logger, os.Getenv("GENIUS_ACCESS_TOKEN"))
	authorization, err := auth.New(pool)
	if err != nil {
		return err
	}

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}

	server := &http.Server{
		Addr: address, Handler: auth.Handler(httpapi.Router(app, logger), authorization, os.Getenv("AUTH_COOKIE_SECURE") == "true"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: nil,
	}

	return serve(ctx, server, app, logger)
}

func initialize(ctx context.Context, uow *store.UnitOfWork) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := uow.Ping(ctx); err != nil {
		return fmt.Errorf("PostgreSQL: %w", err)
	}

	if err := uow.Migrate(ctx); err != nil {
		return err
	}

	if err := uow.Settings.Seed(ctx); err != nil {
		return err
	}

	s, err := uow.Settings.Get(ctx)
	if err != nil {
		return err
	}

	return s.Validate()
}

func serve(ctx context.Context, server *http.Server, app *service.App, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workersDone := make(chan struct{})

	go func() {
		defer close(workersDone)
		app.RunWorkers(ctx)
	}()

	serverDone := make(chan error, 1)

	go func() { serverDone <- server.ListenAndServe() }()
	logger.Info("HTTP API запущен", "address", server.Addr)

	var result error
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	cancel()

	shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()

	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, err, server.Close())
	}

	<-workersDone

	return result
}
