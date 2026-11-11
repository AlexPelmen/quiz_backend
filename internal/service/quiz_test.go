package service

import (
	"math"
	"testing"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

func TestClipBounds(t *testing.T) {
	for _, duration := range []float64{0.5, 7, 29.99, 30, 30.01, 185.7} {
		for _, random := range []float64{0, 0.001, 0.5, 0.999999} {
			start, finish := ClipBounds(duration, random)
			if start < 0 || finish > duration || math.Abs(finish-start-math.Min(duration, 30)) > 1e-9 {
				t.Fatalf("duration=%f random=%f: [%f,%f]", duration, random, start, finish)
			}

			if duration <= 30 && start != 0 {
				t.Fatalf("короткая запись должна начинаться с нуля: %v", start)
			}
		}
	}
}

func TestParseTrackLimit(t *testing.T) {
	if got := ParseTrackLimit(nil); got != 1 {
		t.Fatalf("неизвестные подписчики: %d", got)
	}

	for i, threshold := range []int64{100_000, 300_000, 500_000, 1_000_000, 2_000_000, 5_000_000, 10_000_000} {
		below := threshold - 1
		if got := ParseTrackLimit(&below); got != i+1 {
			t.Errorf("%d подписчиков: %d треков", below, got)
		}

		if got := ParseTrackLimit(&threshold); got != i+2 {
			t.Errorf("%d подписчиков: %d треков", threshold, got)
		}
	}
}

func TestVowelHint(t *testing.T) {
	if got := vowelHint("Ёлка — Hello, мир!"); got != "ёа — eo, и!" {
		t.Fatalf("неожиданная подсказка: %q", got)
	}
}

func TestQuestionHintsAndAnswerInfo(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Song (Live) Again (2020)", "o aai"},
		{"Song (Live (Remastered))", "o"},
		{"Song (unfinished", "o (uiie"},
		{"Song XZ _Again", "o aai"},
	} {
		if got := vowelHint(tc.title); got != tc.want {
			t.Errorf("vowelHint(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}

	for _, tc := range []struct{ name, want string }{
		{"The Doors", "D/Д"},
		{"The Beatles", "B/Б"},
		{"A Day To Remember", "A/А"},
		{"An Endless Sporadic", "A/А"},
		{"tHe  Cure", "C/К"},
		{"Therapy?", "T/Т"},
		{"The", "T/Т"},
		{"Кино", "K/К"},
		{"Жуки", "Zh/Ж"},
	} {
		if got := artistInitial(domain.Candidate{Artist: domain.Artist{Name: tc.name}}).SecondaryText; got != tc.want {
			t.Errorf("initial(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	c := domain.Candidate{Artist: domain.Artist{Name: "Artist"}, Track: domain.Track{Name: "Song"}, Album: domain.Album{Name: "Album"}}

	q, err := makeQuestion("artist_by_logo", c)
	if err != nil || q.AnswerInfo["artist"] != "Artist" || q.AnswerInfo["track"] != "Song" || q.AnswerInfo["album"] != "Album" {
		t.Fatalf("answer info: %#v, %v", q.AnswerInfo, err)
	}
}

func TestCitySequenceBacktracks(t *testing.T) {
	names := []string{"The Аб", "Bz", "Bc", "Cd", "De", "Ef", "Fg", "Gh", "Hi", "Ij", "Jk", "Sum-41", "Måneskin"}
	artists := make([]domain.Artist, 0, len(names))

	for _, name := range names {
		artists = append(artists, domain.Artist{ID: uuid.New(), Name: name})
	}

	if item := cityName(artists[11]); item.first != 0 {
		t.Fatal("название с цифрой на конце попало в цепочку")
	}

	sequence := citySequence(t.Context(), artists)
	if len(sequence) != 10 || sequence[0].artist.Name != "The Аб" || sequence[1].artist.Name != "Bc" {
		t.Fatalf("не найдена цепочка с откатом от тупика Bz: %+v", sequence)
	}

	for i := 1; i < len(sequence); i++ {
		if sequence[i-1].last != sequence[i].first {
			t.Fatalf("цепочка разорвана между %q и %q", sequence[i-1].artist.Name, sequence[i].artist.Name)
		}
	}
}
