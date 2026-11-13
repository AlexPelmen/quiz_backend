package service

import (
	"math"
	"strings"
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

func TestQuestionsRemoveParentheticalNames(t *testing.T) {
	c := domain.Candidate{
		Artist:     domain.Artist{Name: "Artist (Live)"},
		Track:      domain.Track{Name: "Song (Live (Remastered))"},
		Album:      domain.Album{Name: "Album (Deluxe)"},
		AlbumNames: []string{"First (Deluxe)", "Second (2024)"},
		Sound:      &domain.Download{Meta: map[string]any{"duration_seconds": float64(60)}},
	}
	for kind := range roundBuilders {
		q, err := makeQuestion(kind, c)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if strings.ContainsAny(q.Answer+q.SecondaryText, "()") {
			t.Errorf("%s: скобки в задании %+v", kind, q)
		}
		if q.AnswerInfo["artist"] != "Artist" || q.AnswerInfo["track"] != "Song" || q.AnswerInfo["album"] != "Album" {
			t.Errorf("%s: ответ %+v", kind, q.AnswerInfo)
		}
	}
	if c.Track.Name != "Song (Live (Remastered))" {
		t.Fatal("изменено исходное название")
	}
	item := sequenceName(domain.Artist{Name: "Band (Guest)"})
	if item.length != 4 || item.last != 'd' {
		t.Fatalf("название в последовательности: %+v", item)
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

	items := make(map[uuid.UUID]sequenceArtist)
	for _, artist := range artists {
		items[artist.ID] = cityName(artist)
	}
	choices := cityChoices(t.Context(), items, 0, 9)
	if !choices[[2]byte{'a', 'b'}] {
		t.Fatal("не найден старт цепочки")
	}
	delete(items, artists[0].ID)
	choices = cityChoices(t.Context(), items, 'b', 8)
	if choices[[2]byte{'b', 'z'}] || !choices[[2]byte{'b', 'c'}] {
		t.Fatalf("тупиковое продолжение не отсеяно: %v", choices)
	}
}

func TestGrowthChoicesLeaveRoomForFullRound(t *testing.T) {
	items := make(map[uuid.UUID]sequenceArtist)
	for _, name := range []string{"A", "The AB", "ABC", "ABCD", "ABCDEF", "ABCDEFGH"} {
		artist := domain.Artist{ID: uuid.New(), Name: name}
		items[artist.ID] = sequenceName(artist)
	}
	allowed := sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{}, 4)
	for _, item := range items {
		want := item.artist.Name == "The AB"
		if allowed(item) != want {
			t.Errorf("старт %q: допустим = %t, ожидалось %t", item.artist.Name, allowed(item), want)
		}
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 4}, 0)
	if !allowed(sequenceArtist{length: 6}) || allowed(sequenceArtist{length: 8}) {
		t.Fatal("не выбран шаг на две буквы")
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 1}, 1)
	if !allowed(sequenceArtist{length: 2}) || allowed(sequenceArtist{length: 4}) {
		t.Fatal("не выбран шаг на одну или две буквы")
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 1}, 0)
	if allowed(sequenceArtist{length: 6}) {
		t.Fatal("дальний переход вытеснил доступный короткий шаг")
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 3}, 0)
	if !allowed(sequenceArtist{length: 4}) || allowed(sequenceArtist{length: 8}) {
		t.Fatal("не выбран ближайший шаг")
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 6}, 0)
	if !allowed(sequenceArtist{length: 8}) {
		t.Fatal("потерян последний доступный вариант")
	}
	for id, item := range items {
		if item.length == 6 {
			delete(items, id)
		}
	}
	allowed = sequenceChoices(t.Context(), "artist_growth", items, sequenceArtist{length: 4}, 0)
	if !allowed(sequenceArtist{length: 8}) {
		t.Fatal("не разрешён дальний переход при отсутствии короткого")
	}
}
