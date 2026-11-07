package service

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

func TestMediaClips(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s отсутствует: %v", name, err)
		}
	}

	s := domain.DefaultSettings()
	s.StorageDir = t.TempDir()

	for _, duration := range []string{"5", "65"} {
		t.Run(duration, func(t *testing.T) { testMediaClip(t, s, duration) })
	}
}

func testMediaClip(t *testing.T, s domain.Settings, duration string) {
	t.Helper()

	ctx := t.Context()
	source := filepath.Join(s.StorageDir, "source-"+duration+".mp3")

	_, err := runCommand(ctx, s.FFmpeg, []string{
		"-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", duration, "-metadata", "title=Секретный ответ", "-metadata", "artist=Секретный исполнитель", source,
	})
	if err != nil {
		t.Fatal(err)
	}

	actual, err := probe(ctx, s.FFprobe, source)
	if err != nil {
		t.Fatal(err)
	}

	start, finish := ClipBounds(actual, 0.5)
	q := domain.Question{ID: uuid.New(), Number: 1, TrackStartSec: &start, TrackFinishSec: &finish}
	m := &Media{}
	input := clipInput{Question: q, Download: domain.Download{ID: uuid.New(), URI: source}}

	path, err := m.clip(ctx, s, input)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.StorageDir, "music", "clips", q.ID.String()+".mp3"); path != want {
		t.Fatalf("неверный путь фрагмента: %s, ожидался %s", path, want)
	}

	result, err := probe(ctx, s.FFprobe, path)
	if err != nil || math.Abs(result-math.Min(actual, 30)) > 0.15 {
		t.Fatalf("длительность результата %v, исходника %v: %v", result, actual, err)
	}

	assertNoAnswerTags(t, ctx, s, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	cached, err := m.clip(ctx, s, input)
	if err != nil || cached != path {
		t.Fatalf("кэш: %s, %v", cached, err)
	}

	after, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(after.ModTime()) {
		t.Fatalf("кэш был пересоздан: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	input.Question.ID = uuid.New()
	if _, err := m.clip(canceled, s, input); err == nil {
		t.Fatal("отменённая операция не должна создавать новый фрагмент")
	}
}

func assertNoAnswerTags(t *testing.T, ctx context.Context, s domain.Settings, path string) {
	t.Helper()

	output, err := runCommand(ctx, s.FFprobe,
		[]string{"-v", "error", "-show_entries", "format_tags", "-of", "json", path})
	if err != nil {
		t.Fatal(err)
	}

	var result struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}

	if result.Format.Tags["artist"] != "" || result.Format.Tags["title"] != "Вопрос 1" {
		t.Fatalf("исходные теги попали в отрывок: %v", result.Format.Tags)
	}
}
