package service

import (
	"math"
	"testing"

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
	for _, tc := range []struct {
		points float64
		k      float64
		want   int
	}{
		{100, 0.1, 10}, {50, 0.1, 5}, {19.99, 0.1, 1}, {1, 0.1, 1}, {0, 0.1, 0}, {100, 0, 0},
	} {
		if got := ParseTrackLimit(tc.k, tc.points); got != tc.want {
			t.Fatalf("k=%v points=%v: got %d, want %d", tc.k, tc.points, got, tc.want)
		}
	}
}

func TestVowelHint(t *testing.T) {
	if got := vowelHint("Ёлка — Hello, мир!"); got != "Ё__а — _e__o, _и_!" {
		t.Fatalf("неожиданная подсказка: %q", got)
	}
}

func TestRoundScore(t *testing.T) {
	for _, tc := range []struct {
		name          string
		score         roundScore
		current, want float64
	}{
		{"correct in a difficult round", roundScore{true, 0.2, 20}, 50, 58},
		{"incorrect in an easy round", roundScore{false, 0.8, 20}, 50, 42},
		{"nothing guessed", roundScore{false, 0, 20}, 50, 50},
		{"everything guessed", roundScore{true, 1, 20}, 50, 50},
		{"disabled", roundScore{true, 0.2, 0}, 50, 50},
		{"rounding near zero", roundScore{false, 0.99, 100}, 0.01, 0.01},
		{"rounding near one hundred", roundScore{true, 0.01, 100}, 99.99, 99.99},
		{"wrong answer cannot raise a manual score", roundScore{false, 0.9, 20}, 0.001, 0.001},
		{"correct answer cannot lower a manual score", roundScore{true, 0.1, 20}, 99.999, 99.999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.score.points(tc.current, 1); got != tc.want {
				t.Fatalf("points = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuestionHintsAndAnswerInfo(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Song (Live) Again (2020)", "_o__ A_ai_"},
		{"Song (Live (Remastered))", "_o__"},
		{"Song (unfinished", "_o__ (u__i_i__e_"},
	} {
		if got := vowelHint(tc.title); got != tc.want {
			t.Errorf("vowelHint(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
	for _, tc := range []struct{ name, want string }{{"The Beatles", "B"}, {"tHe  Cure", "C"}, {"Therapy?", "T"}, {"The", "T"}, {"Кино", "К"}} {
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
