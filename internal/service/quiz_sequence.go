package service

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

const roundSize = 10

type sequenceArtist struct {
	artist domain.Artist
	first  byte
	last   byte
	length int
}

func buildSequenceRound(
	ctx context.Context, packs store.PackRepository, in roundInput, round domain.QuizRound,
) (domain.QuizRound, error) {
	filter := in.pickFilter()
	items := make(map[uuid.UUID]sequenceArtist)
	for _, track := range in.Audio {
		if slices.Contains(in.Used, track.ArtistID) {
			continue
		}
		if _, ok := items[track.ArtistID]; !ok {
			artist := domain.Artist{ID: track.ArtistID, Name: track.ArtistName}
			item := sequenceName(artist)
			if in.Kind == "artist_cities" {
				item = cityName(artist)
			}
			items[track.ArtistID] = item
		}
	}

	var previous sequenceArtist
	for i := range roundSize {
		remaining := roundSize - i - 1
		matches := sequenceChoices(ctx, in.Kind, items, previous, remaining)
		if err := ctx.Err(); err != nil {
			return round, err
		}

		tracks := make([]store.AudioTrack, 0, len(in.Audio))
		for _, track := range in.Audio {
			if item, ok := items[track.ArtistID]; ok && matches(item) {
				tracks = append(tracks, track)
			}
		}
		track, ok := filter.PickAudioTrack(tracks)
		if !ok {
			return round, domain.ErrNotFound
		}

		c, err := packs.AudioCandidate(ctx, track)
		if err != nil {
			return round, err
		}
		q, err := makeQuestion(in.Kind, c)
		if err != nil {
			return round, err
		}

		appendQuestion(&round, q, i)
		previous = items[track.ArtistID]
		delete(items, track.ArtistID)
	}

	return round, nil
}

func sequenceName(artist domain.Artist) sequenceArtist {
	item := sequenceArtist{artist: artist}
	cleanName := cleanQuestionName(artist.Name)
	name := latinArtistName(cleanName)

	countName := cleanName
	if first, rest, ok := strings.Cut(cleanName, " "); ok && strings.EqualFold(first, "the") {
		countName = rest
	}
	for _, r := range countName {
		if unicode.IsLetter(r) {
			item.length++
		}
	}

	for _, r := range name {
		if r < 'a' || r > 'z' {
			continue
		}

		if item.first == 0 {
			item.first = byte(r)
		}

		item.last = byte(r)
	}

	return item
}

func withoutArticle(name string) string {
	parts := strings.Fields(name)
	if len(parts) > 1 {
		switch strings.ToLower(parts[0]) {
		case "the", "a", "an":
			return strings.Join(parts[1:], " ")
		}
	}

	return strings.TrimSpace(name)
}

func latinArtistName(name string) string {
	return norm.NFD.String(domain.TransliterateRussian(strings.ToLower(norm.NFC.String(name))))
}

func initialHint(name string) string {
	const latin = "abcdefghijklmnopqrstuvwxyz"

	russian := [...]string{
		"А", "Б", "К", "Д", "Е", "Ф", "Г", "Х", "И", "Дж", "К", "Л", "М",
		"Н", "О", "П", "К", "Р", "С", "Т", "У", "В", "В", "Кс", "Й", "З",
	}

	parts := strings.Fields(name)
	if len(parts) > 1 && strings.EqualFold(parts[0], "the") {
		name = strings.Join(parts[1:], " ")
	}
	for _, letter := range norm.NFC.String(name) {
		if !unicode.IsLetter(letter) {
			continue
		}

		original := string(unicode.ToUpper(letter))
		lower := string(unicode.ToLower(letter))
		transliterated := domain.TransliterateRussian(lower)

		if transliterated != lower && transliterated != "" {
			return strings.ToUpper(transliterated[:1]) + transliterated[1:] + "/" + original
		}

		base, _ := utf8.DecodeRuneInString(latinArtistName(lower))
		if i := strings.IndexRune(latin, base); i >= 0 {
			return string(unicode.ToUpper(base)) + "/" + russian[i]
		}

		return original
	}

	return ""
}

func cityName(artist domain.Artist) sequenceArtist {
	name := latinArtistName(withoutArticle(cleanQuestionName(artist.Name)))
	// Знаки оформления пропускаем, цифры на краях оставляем, чтобы не подменять их буквами.
	trimmed := strings.TrimFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	first, _ := utf8.DecodeRuneInString(trimmed)
	last, _ := utf8.DecodeLastRuneInString(trimmed)

	if first < 'a' || first > 'z' || last < 'a' || last > 'z' {
		return sequenceArtist{}
	}

	return sequenceArtist{artist: artist, first: byte(first), last: byte(last)}
}

func sequenceChoices(
	ctx context.Context, kind string, items map[uuid.UUID]sequenceArtist, previous sequenceArtist, remaining int,
) func(sequenceArtist) bool {
	if kind == "artist_cities" {
		edges := cityChoices(ctx, items, previous.last, remaining)
		return func(item sequenceArtist) bool { return edges[[2]byte{item.first, item.last}] }
	}

	key := func(item sequenceArtist) int { return int(item.first) }
	if kind == "artist_growth" {
		key = func(item sequenceArtist) int { return item.length }
	}

	minimum := key(previous)
	values := make(map[int]bool)
	for _, item := range items {
		if value := key(item); value > minimum {
			values[value] = true
		}
	}

	ordered := make([]int, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	slices.Sort(ordered)
	if len(ordered) <= remaining {
		return func(sequenceArtist) bool { return false }
	}

	// Более поздний старт должен оставлять достаточно разных значений для конца раунда.
	maximum := ordered[len(ordered)-remaining-1]
	if kind == "artist_growth" {
		low, high := minimum+1, minimum+2
		if minimum == 0 {
			low, high = 2, 4
		}
		for value := low; value <= min(high, maximum); value++ {
			if values[value] {
				minimum, maximum = low-1, min(high, maximum)
				break
			}
		}
	}

	return func(item sequenceArtist) bool {
		value := key(item)
		return value > minimum && value <= maximum
	}
}

func cityChoices(
	ctx context.Context, items map[uuid.UUID]sequenceArtist, required byte, remaining int,
) map[[2]byte]bool {
	type edge = [2]byte
	counts := make(map[edge]int)
	byFirst := make(map[byte][]edge)
	for _, item := range items {
		if item.first == 0 {
			continue
		}
		e := edge{item.first, item.last}
		if counts[e] == 0 {
			byFirst[item.first] = append(byFirst[item.first], e)
		}
		counts[e]++
	}

	budget := 100000
	var canFinish func(byte, int) bool
	canFinish = func(first byte, left int) bool {
		if left == 0 {
			return true
		}
		if budget <= 0 || ctx.Err() != nil {
			return false
		}
		budget--

		for _, e := range byFirst[first] {
			if counts[e] == 0 {
				continue
			}
			counts[e]--
			ok := canFinish(e[1], left-1)
			counts[e]++
			if ok {
				return true
			}
		}
		return false
	}

	choices := make(map[edge]bool)
	for e := range counts {
		if required != 0 && e[0] != required {
			continue
		}
		counts[e]--
		choices[e] = canFinish(e[1], remaining)
		counts[e]++
	}
	return choices
}
