package service

import (
	"context"
	"devkeeper.com/quiz/internal/domain"
	"errors"
	"slices"
	"testing"
)

func TestSelectRoundTypes(t *testing.T) {
	kinds := []string{"audio", "cover", "logo"}
	for _, count := range []int{1, 2, 3, 7, 10} {
		selected := selectRoundTypes(kinds, count)
		if len(selected) != count {
			t.Fatalf("получено %d раундов, ожидалось %d", len(selected), count)
		}
		for start := 0; start < count; start += len(kinds) {
			seen := map[string]bool{}
			for _, kind := range selected[start:min(start+len(kinds), count)] {
				if !slices.Contains(kinds, kind) || seen[kind] {
					t.Fatalf("неверный набор в цикле: %v", selected)
				}
				seen[kind] = true
			}
		}
	}
	if !slices.Equal(kinds, []string{"audio", "cover", "logo"}) {
		t.Fatal("изменён список выбранных типов")
	}
	if got := selectRoundTypes([]string{"audio", "audio"}, 3); !slices.Equal(got, []string{"audio", "audio", "audio"}) {
		t.Fatalf("повторы настроек: %v", got)
	}
}

func TestGenerateRejectsInvalidQuestionCount(t *testing.T) {
	app := &App{}
	for _, count := range []int{-10, 0, 1, 15, 101, 110} {
		_, err := app.Generate(context.Background(), PackForm{Name: "Тест", QuestionCount: &count}, nil)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("количество %d: ожидалась ошибка формы, получено %v", count, err)
		}
	}
}

func TestRoundTypesAreShuffled(t *testing.T) {
	kinds := []string{"audio", "cover", "logo"}
	first := selectRoundTypes(kinds, 2)
	for range 100 {
		if !slices.Equal(first, selectRoundTypes(kinds, 2)) {
			return
		}
	}
	t.Fatal("порядок раундов не меняется")
}
