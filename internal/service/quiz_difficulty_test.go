package service

import (
	"devkeeper.com/quiz/internal/domain"
	"testing"
)

func TestRoundDifficultyCenter(t *testing.T) {
	for _, tc := range []struct {
		difficulty string
		want       float64
	}{
		{"easy", 0}, {"medium", 0.35}, {"hard", 0.8},
	} {
		t.Run(tc.difficulty, func(t *testing.T) {
			in := roundInput{Pack: domain.QuizPack{Difficulty: tc.difficulty}}
			filter := in.pickFilter()
			if filter.Center != tc.want || !filter.CenterSet {
				t.Fatalf("параметры выбора: %+v, ожидается центр %v", filter, tc.want)
			}
		})
	}
}
