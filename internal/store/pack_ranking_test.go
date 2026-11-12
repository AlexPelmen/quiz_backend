package store

import "testing"

func TestRankCandidatesCenter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter PickFilter
		want   float64
	}{
		{"top of ranking", PickFilter{Center: 0, CenterSet: true}, 0},
		{"medium", PickFilter{Center: 0.35, CenterSet: true}, 0.35},
		{"hard", PickFilter{Center: 0.8, CenterSet: true}, 0.8},
		{"default", PickFilter{}, 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, args := rankCandidates("SELECT * FROM tracks", "e.*", tc.filter, nil)
			if got := args[len(args)-1]; got != tc.want {
				t.Fatalf("центр: %v, ожидается %v", got, tc.want)
			}
		})
	}
}
