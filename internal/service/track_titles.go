package service

import (
	"strings"
	"unicode"

	"devkeeper.com/quiz/internal/source"
)

// trackTitleKey removes balanced parenthesized parts for duplicate detection.
func trackTitleKey(title string) string {
	var result strings.Builder
	var group strings.Builder
	depth := 0
	for _, r := range title {
		switch r {
		case '(':
			group.WriteRune(r)
			depth++
		case ')':
			if depth > 0 {
				depth--
				group.WriteRune(r)
				if depth == 0 {
					group.Reset()
				}
			} else {
				result.WriteRune(r)
			}
		default:
			if depth == 0 {
				result.WriteRune(unicode.ToLower(r))
			} else {
				group.WriteRune(unicode.ToLower(r))
			}
		}
	}
	result.WriteString(group.String())
	return strings.Join(strings.Fields(result.String()), " ")
}

func uniqueTrackTitles(tracks []source.Song) []source.Song {
	seen := make(map[string]bool)
	result := make([]source.Song, 0, len(tracks))
	for _, track := range tracks {
		key := trackTitleKey(track.Title)
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		result = append(result, track)
	}
	return result
}
