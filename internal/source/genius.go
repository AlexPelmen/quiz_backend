package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"devkeeper.com/quiz/internal/domain"
)

// Genius адаптирует официальный и веб-API Genius к общему каталогу.
type Genius struct {
	api jsonClient
	web jsonClient
}

// SetQuotaObserver получает заголовки лимита официального Genius API.
func (g *Genius) SetQuotaObserver(fn func(context.Context, domain.GeniusQuota) error) {
	g.api.onQuota = fn
}

// RefreshQuota выполняет один запрос к официальному API для получения текущего лимита.
func (g *Genius) RefreshQuota(ctx context.Context) error {
	var response json.RawMessage

	g.api.retries = 0

	return g.api.get(ctx, "/artists/1436", &response)
}

// NewGenius создаёт клиент; секрет отправляется только официальному API.
func NewGenius(s domain.Settings, token string) *Genius {
	client := &http.Client{Timeout: time.Duration(s.HTTPTimeoutSeconds) * time.Second}

	return &Genius{
		api: jsonClient{
			client: client, base: s.GeniusURL, token: token,
			userAgent: s.UserAgent, interval: time.Duration(s.GeniusRequestIntervalMS) * time.Millisecond,
			retryStatus: http.StatusTooManyRequests, retries: min(3, s.Source429Retries),
			retryDelay:    time.Duration(s.Source429DelaySeconds) * time.Second,
			maxRetryDelay: time.Duration(s.Source429MaxDelaySeconds) * time.Second,
		},
		web: jsonClient{
			client: client, base: s.GeniusWebURL,
			userAgent: s.UserAgent, interval: time.Duration(s.GeniusRequestIntervalMS) * time.Millisecond,
			retryStatus: http.StatusTooManyRequests, retries: min(3, s.Source429Retries),
			retryDelay:    time.Duration(s.Source429DelaySeconds) * time.Second,
			maxRetryDelay: time.Duration(s.Source429MaxDelaySeconds) * time.Second,
		},
	}
}

func (g *Genius) artistID(ctx context.Context, lookup Lookup) (int64, error) {
	if lookup.GeniusID != nil {
		return *lookup.GeniusID, nil
	}

	matches, err := g.searchArtists(ctx, lookup.Name, lookup.Name)
	if err != nil {
		return 0, err
	}

	// Если Genius не индексирует кириллическое название, ищем его транслитерацию.
	if len(matches) == 0 {
		query := domain.TransliterateRussian(normalizeArtistName(lookup.Name))
		if query != normalizeArtistName(lookup.Name) {
			matches, err = g.searchArtists(ctx, query, lookup.Name)
			if err != nil {
				return 0, err
			}
		}
	}

	if len(matches) == 0 {
		query := compactArtistName(normalizeArtistName(lookup.Name))
		if query != "" && !strings.EqualFold(query, strings.TrimSpace(lookup.Name)) {
			matches, err = g.searchArtists(ctx, query, lookup.Name)
			if err != nil {
				return 0, err
			}
		}
	}

	if len(matches) == 0 {
		return 0, fmt.Errorf("Genius: исполнитель %q не найден среди основных исполнителей найденных песен", lookup.Name)
	}

	if len(matches) > 1 {
		return 0, fmt.Errorf("Genius: несколько исполнителей соответствуют имени %q", lookup.Name)
	}

	for id := range matches {
		return id, nil
	}

	return 0, domain.ErrNotFound
}

func (g *Genius) searchArtists(ctx context.Context, search, name string) (map[int64]bool, error) {
	s := newGeniusArtistSearch(search, name)
	for page := 1; page <= 5; page++ {
		more, err := g.SearchArtistPage(ctx, s, page)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	for _, ids := range s.matches {
		if len(ids) > 0 {
			return ids, nil
		}
	}
	return nil, nil
}

// SearchArtistPage добавляет совпадения из одного запроса к поиску Genius.
func (g *Genius) SearchArtistPage(ctx context.Context, s *GeniusArtistSearch, page int) (bool, error) {
	const pageSize = 10
	var result struct {
		Response struct {
			Hits []struct {
				Result struct {
					PrimaryArtist struct {
						ID   int64  `json:"id"`
						Name string `json:"name"`
					} `json:"primary_artist"`
				} `json:"result"`
			} `json:"hits"`
		} `json:"response"`
	}

	path := fmt.Sprintf("/search?q=%s&per_page=%d&page=%d", url.QueryEscape(s.query), pageSize, page)
	if err := g.api.get(ctx, path, &result); err != nil {
		return false, err
	}

	name, matches := s.name, s.matches
	for _, hit := range result.Response.Hits {
		a := hit.Result.PrimaryArtist
		if a.ID == 0 {
			continue
		}
		normalizedName, normalizedQuery := normalizeArtistName(a.Name), normalizeArtistName(name)
		switch {
		case strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name)):
			matches[0][a.ID] = true
		case normalizedName == normalizedQuery:
			matches[1][a.ID] = true
		case normalizedQuery != "" && compactArtistName(normalizedName) == compactArtistName(normalizedQuery):
			matches[2][a.ID] = true
		case domain.TransliterateRussian(normalizedName) == domain.TransliterateRussian(normalizedQuery):
			matches[3][a.ID] = true
		case artistNameMatches(a.Name, name):
			matches[4][a.ID] = true
		}
	}

	return len(result.Response.Hits) == pageSize, nil
}

func artistNameMatches(name, query string) bool {
	name, query = normalizeArtistName(name), normalizeArtistName(query)
	return containsArtistWords(name, query) ||
		containsArtistWords(domain.TransliterateRussian(name), domain.TransliterateRussian(query))
}

func containsArtistWords(name, query string) bool {
	if query == "" {
		return false
	}
	for offset := 0; offset <= len(name)-len(query); {
		index := strings.Index(name[offset:], query)
		if index < 0 {
			return false
		}
		index += offset
		before := []rune(name[:index])
		after := []rune(name[index+len(query):])
		if (len(before) == 0 || !unicode.IsLetter(before[len(before)-1]) && !unicode.IsDigit(before[len(before)-1])) &&
			(len(after) == 0 || !unicode.IsLetter(after[0]) && !unicode.IsDigit(after[0])) {
			return true
		}
		offset = index + len(query)
	}
	return false
}

func normalizeArtistName(name string) string {
	var plain strings.Builder
	depth := 0
	for _, r := range name {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			plain.WriteRune(r)
		}
	}
	name = plain.String()

	name = norm.NFD.String(strings.ToLower(name))
	name = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) || r == '.' {
			return -1
		}

		if r == '’' || r == '‘' || r == '`' {
			return '\''
		}

		return r
	}, name)

	return strings.Join(strings.Fields(name), " ")
}

func compactArtistName(name string) string {
	return strings.Join(strings.Fields(name), "")
}

// Catalog добирает треки и заполняет пропуски в метаданных.
func (g *Genius) Catalog(ctx context.Context, lookup Lookup) (Catalog, error) {
	var c Catalog
	if lookup.AlbumLimit == 0 {
		lookup.AlbumLimit = StudioAlbumLimit
	}

	var id int64

	var err error
	if lookup.NeedArtist || lookup.TrackLimit > 0 || needsGeniusAlbumList(lookup.Base.Albums, lookup.AlbumLimit) {
		id, err = g.artistID(ctx, lookup)
		if err != nil {
			return c, err
		}

		c.ArtistID = id
	}

	if lookup.NeedArtist {
		var artist struct {
			Response struct {
				Artist struct {
					Name     string `json:"name"`
					ImageURL string `json:"image_url"`
				} `json:"artist"`
			} `json:"response"`
		}

		if err := g.api.get(ctx, fmt.Sprintf("/artists/%d", id), &artist); err != nil {
			c.Warnings = append(c.Warnings, err.Error())
		} else {
			c.Name, c.LogoURL = artist.Response.Artist.Name, artist.Response.Artist.ImageURL
		}
	}

	if lookup.TrackLimit > 0 {
		c.Tracks, err = g.songs(ctx, id, lookup.TrackLimit, lookup.Base.Tracks)
		if err != nil {
			c.Warnings = append(c.Warnings, err.Error())
		}
	}

	c.Albums, err = g.fillAlbums(ctx, id, lookup)

	return c, err
}

func (g *Genius) fillAlbums(ctx context.Context, id int64, lookup Lookup) ([]Album, error) {
	existing := lookup.Base.Albums
	if !NeedsAlbums(existing, lookup.AlbumLimit) {
		return nil, nil
	}

	var candidates []Album

	if needsGeniusAlbumList(existing, lookup.AlbumLimit) {
		var response struct {
			Response struct {
				Albums []Album `json:"albums"`
			} `json:"response"`
		}

		path := fmt.Sprintf("/artists/%d/albums?per_page=20&page=1", id)
		if err := g.web.get(ctx, path, &response); err != nil {
			return nil, err
		}

		candidates = response.Response.Albums
	}

	var updates []Album

	slots := lookup.AlbumLimit - len(existing)

	for _, album := range candidates {
		if album.ID == 0 || !IsStudioAlbum(album) {
			continue
		}

		matches := slices.IndexFunc(existing, func(old Album) bool { return SameAlbum(old, album) })
		if matches >= 0 {
			old := existing[matches]
			if !missingAlbumFields(old) || old.ID != 0 {
				continue
			}

			album = fillGeniusAlbumSummary(old, album)
		} else {
			if !strings.EqualFold(album.Type, "Album") && !knownStudioAlbum(album, lookup.StudioGroups) {
				continue
			}

			if slots <= 0 || slices.ContainsFunc(updates, func(old Album) bool { return SameAlbum(old, album) }) {
				continue
			}

			slots--
		}

		updates = append(updates, album)
	}

	for _, album := range existing {
		if album.ID != 0 && missingAlbumFields(album) {
			updates = append(updates, album)
		}
	}

	var issues error

	for i := range updates {
		if err := g.albumDetails(ctx, &updates[i]); err != nil {
			issues = errors.Join(issues, err)
		}
	}

	return updates, issues
}

func needsGeniusAlbumList(existing []Album, limit int) bool {
	return len(existing) < limit || slices.ContainsFunc(existing, func(album Album) bool {
		return album.ID == 0 && missingAlbumFields(album)
	})
}

func knownStudioAlbum(album Album, groups []Album) bool {
	matches := 0
	studio := false

	for _, group := range groups {
		if SameAlbum(album, group) {
			matches++
			studio = IsStudioAlbum(group)
		}
	}

	return matches == 1 && studio
}

func fillGeniusAlbumSummary(old, summary Album) Album {
	if old.ID == 0 {
		old.ID = summary.ID
	}

	if old.CoverURL == "" {
		old.CoverURL = summary.CoverURL
	}

	if old.ReleaseDate == "" {
		old.ReleaseDate = summary.ReleaseDate
	}

	return old
}

func (g *Genius) albumDetails(ctx context.Context, album *Album) error {
	if !missingAlbumFields(*album) {
		return nil
	}

	var response struct {
		Response struct {
			Album Album `json:"album"`
		} `json:"response"`
	}

	if err := g.web.get(ctx, fmt.Sprintf("/albums/%d", album.ID), &response); err != nil {
		return err
	}

	*album = fillGeniusAlbumSummary(*album, response.Response.Album)
	return nil
}

func (g *Genius) songs(ctx context.Context, id int64, limit int, existing []Song) ([]Song, error) {
	r := []Song{}
	for page := 1; len(r) < limit && page <= 2; {
		var response struct {
			Response struct {
				Songs    []Song `json:"songs"`
				NextPage *int   `json:"next_page"`
			} `json:"response"`
		}

		path := fmt.Sprintf("/artists/%d/songs?sort=popularity&per_page=50&page=%d", id, page)
		if err := g.api.get(ctx, path, &response); err != nil {
			return nil, err
		}

		for _, song := range response.Response.Songs {
			if song.ID == 0 {
				continue
			}

			if song.Title == "" {
				detail, err := g.song(ctx, song.ID)
				if err != nil {
					return r, err
				}

				song = detail
			}

			if song.Title == "" || slices.ContainsFunc(existing, func(old Song) bool { return sameSongTitle(old, song) }) ||
				slices.ContainsFunc(r, func(old Song) bool { return sameSongTitle(old, song) }) {
				continue
			}

			r = append(r, song)
			if len(r) == limit {
				break
			}
		}

		if response.Response.NextPage == nil || *response.Response.NextPage <= page {
			break
		}

		page = *response.Response.NextPage
	}

	return r, nil
}
