package service

import (
	"devkeeper.com/quiz/internal/source"
	"testing"
)

func TestUniqueTrackTitles(t *testing.T) {
	tracks := []source.Song{{Title: "TrackName"}, {Title: "trackname (Right Version)"}, {Title: "TrackName (Extended version)"}, {Title: "TrackName (Life in Berlin 1945)"}, {Title: "Other (Live)"}, {Title: "Other"}}
	got := uniqueTrackTitles(tracks)
	if len(got) != 2 || got[0].Title != "TrackName" || got[1].Title != "Other (Live)" {
		t.Fatalf("unexpected tracks: %+v", got)
	}
	if trackTitleKey("A (live) B") != trackTitleKey("a b") {
		t.Fatal("parenthesized part was not removed")
	}
}
