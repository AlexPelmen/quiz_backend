package service

import (
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/testutil"
	"github.com/google/uuid"
)

func TestStorageActionsAttachAndDelete(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	settings, err := uow.Settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.StorageDir = t.TempDir()
	if err := uow.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	artist := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO artists(id,name) VALUES($1,'Test Artist')`, artist); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(settings.StorageDir, "images", "artists")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "Test Artist.png")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	results, err := app.StorageAction(ctx, "attach", []string{"images/artists/Test Artist.png"})
	if err != nil || len(results) != 1 || results[0].Error != "" {
		t.Fatalf("привязка: %+v %v", results, err)
	}
	var downloadID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT logo_download_id FROM artists WHERE id=$1`, artist).Scan(&downloadID); err != nil || downloadID == nil {
		t.Fatalf("логотип: %v %v", downloadID, err)
	}
	results, err = app.StorageAction(ctx, "delete", []string{"images/artists/Test Artist.png"})
	if err != nil || results[0].Error == "" {
		t.Fatalf("занятый файл должен быть защищён: %+v %v", results, err)
	}
	orphanID := uuid.New()
	orphan := filepath.Join(dir, "orphan.png")
	if err := os.WriteFile(orphan, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := uow.Downloads.SaveUpload(ctx, domain.Download{ID: orphanID, URI: orphan, FileType: "png", Meta: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	results, err = app.StorageAction(ctx, "delete", []string{"images/artists/orphan.png"})
	if err != nil || results[0].Error != "" {
		t.Fatalf("удаление: %+v %v", results, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("файл остался: %v", err)
	}
	if _, err := uow.Downloads.Get(ctx, orphanID); err == nil {
		t.Fatal("загрузка осталась")
	}
}

func TestStoragePathRejectsEscape(t *testing.T) {
	if _, err := storagePath(t.TempDir(), "../outside"); err == nil {
		t.Fatal("путь за пределами хранилища допущен")
	}
}
