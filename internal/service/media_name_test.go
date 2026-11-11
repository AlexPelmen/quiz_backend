package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestMediaName(t *testing.T) {
	if got := mediaName("Группа", "Песня", true); got != "Группа - превью Песня" {
		t.Fatal(got)
	}
	if got := mediaName("A/B", "Трек: 1", false); got != "A_B - Трек_ 1" {
		t.Fatal(got)
	}
}

func TestMediaNameCollisionDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "Группа - Песня.mp3")
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := linkMediaFile(input, dir, "Группа - Песня", "mp3", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(existing)
	if err != nil || string(old) != "old" || path == existing {
		t.Fatalf("collision: %s %s %v", path, old, err)
	}
}
