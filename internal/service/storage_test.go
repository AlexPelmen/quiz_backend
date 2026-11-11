package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"devkeeper.com/quiz/internal/store"
	"github.com/google/uuid"
)

func TestScanStorageRespectsReferencesAndClipCache(t *testing.T) {
	root := t.TempDir()
	question := uuid.New()
	files := []string{"music/tracks/used.mp3", "music/tracks/orphan.mp3", "music/clips/" + question.String() + ".mp3", "music/clips/Группа - превью Песня - " + question.String() + ".mp3", ".download-working/partial"}
	for _, name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ref := filepath.Join(root, "music", "tracks", "used.mp3")
	report, err := scanStorage(context.Background(), root, []store.StoredReference{{URI: ref, Used: true}}, []uuid.UUID{question})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.Count != 4 || report.Total.Bytes != 16 || report.Kinds["tracks"].Count != 2 {
		t.Fatalf("неверная сводка: %+v", report)
	}
	if len(report.Orphans) != 1 || report.Orphans[0].Path != "music/tracks/orphan.mp3" {
		t.Fatalf("неверный список сирот: %+v", report.Orphans)
	}
}
