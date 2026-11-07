package service

import (
	"context"
	"fmt"
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
	artists, err := packs.AudioArtists(ctx, in.Used, filter)
	if err != nil {
		return round, err
	}

	sequence := pickSequence(ctx, in.Kind, artists)

	if err := ctx.Err(); err != nil {
		return round, err
	}

	if len(sequence) != roundSize {
		return round, domain.ErrNotFound
	}

	for i, item := range sequence {
		c, err := packs.PickTrack(ctx, domain.Candidate{Artist: item.artist}, filter)
		if err != nil {
			return round, err
		}

		q, err := makeQuestion(in.Kind, c)
		if err != nil {
			return round, err
		}

		if in.Kind == "artist_growth" {
			q.SecondaryText = fmt.Sprintf("Букв в названии: %d", item.length)
		}

		appendQuestion(&round, q, i)
	}

	return round, nil
}

func sequenceName(artist domain.Artist) sequenceArtist {
	item := sequenceArtist{artist: artist}
	name := latinArtistName(artist.Name)

	for _, r := range artist.Name {
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

	for _, letter := range norm.NFC.String(withoutArticle(name)) {
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
	name := latinArtistName(withoutArticle(artist.Name))
	// Знаки оформления пропускаем, цифры на краях оставляем, чтобы не подменять их буквами.
	trimmed := strings.TrimFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	first, _ := utf8.DecodeRuneInString(trimmed)
	last, _ := utf8.DecodeLastRuneInString(trimmed)

	if first < 'a' || first > 'z' || last < 'a' || last > 'z' {
		return sequenceArtist{}
	}

	return sequenceArtist{artist: artist, first: byte(first), last: byte(last)}
}

func pickSequence(ctx context.Context, kind string, artists []domain.Artist) []sequenceArtist {
	if kind == "artist_cities" {
		return citySequence(ctx, artists)
	}

	key := func(item sequenceArtist) int { return int(item.first) }
	if kind == "artist_growth" {
		key = func(item sequenceArtist) int { return item.length }
	}

	seen := map[int]bool{}
	result := make([]sequenceArtist, 0, roundSize)

	for _, artist := range artists {
		item := sequenceName(artist)
		k := key(item)

		if k == 0 || seen[k] {
			continue
		}

		seen[k] = true

		result = append(result, item)

		if len(result) == roundSize {
			break
		}
	}

	slices.SortFunc(result, func(a, b sequenceArtist) int { return key(a) - key(b) })

	return result
}

func citySequence(ctx context.Context, artists []domain.Artist) []sequenceArtist {
	byFirst := map[byte][]sequenceArtist{}
	counts := map[[2]byte]int{}
	starts := []byte{}

	for _, artist := range artists {
		item := cityName(artist)
		if item.first == 0 {
			continue
		}

		edge := [2]byte{item.first, item.last}
		if counts[edge] == roundSize {
			continue
		}

		counts[edge]++

		if len(byFirst[item.first]) == 0 {
			starts = append(starts, item.first)
		}

		byFirst[item.first] = append(byFirst[item.first], item)
	}

	path := make([]sequenceArtist, 0, roundSize)
	used := map[uuid.UUID]bool{}
	budget := 100000

	var visit func(byte) bool

	visit = func(first byte) bool {
		if len(path) == roundSize {
			return true
		}

		if budget <= 0 || ctx.Err() != nil {
			return false
		}

		budget--
		tried := map[byte]bool{}

		for _, item := range byFirst[first] {
			if used[item.artist.ID] || tried[item.last] {
				continue
			}

			// Исполнители с одинаковыми крайними буквами взаимозаменяемы для поиска цепочки.
			tried[item.last] = true
			used[item.artist.ID] = true

			path = append(path, item)

			if visit(item.last) {
				return true
			}

			path = path[:len(path)-1]

			delete(used, item.artist.ID)
		}

		return false
	}

	for _, first := range starts {
		if visit(first) {
			return path
		}
	}

	return nil
}
