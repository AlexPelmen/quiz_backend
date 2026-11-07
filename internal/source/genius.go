package source

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
			retries: s.Source429Retries, retryDelay: time.Duration(s.Source429DelaySeconds) * time.Second,
			maxRetryDelay: time.Duration(s.Source429MaxDelaySeconds) * time.Second,
		},
		web: jsonClient{
			client: client, base: s.GeniusWebURL,
			userAgent: s.UserAgent, interval: time.Duration(s.GeniusRequestIntervalMS) * time.Millisecond,
			retries: s.Source429Retries, retryDelay: time.Duration(s.Source429DelaySeconds) * time.Second,
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
		query := transliterate(normalizeArtistName(lookup.Name))
		if query != normalizeArtistName(lookup.Name) {
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
	// Полное совпадение надёжнее совпадения по части имени.
	matches := [4]map[int64]bool{{}, {}, {}, {}}

	// Поиск Genius возвращает песни, поэтому исполнитель может появиться не сразу.
	const searchPages = 5
	const pageSize = 10
	for page := 1; page <= searchPages; page++ {
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

		path := fmt.Sprintf("/search?q=%s&per_page=%d&page=%d", url.QueryEscape(search), pageSize, page)
		if err := g.api.get(ctx, path, &result); err != nil {
			return nil, err
		}

		for _, hit := range result.Response.Hits {
			a := hit.Result.PrimaryArtist
			normalizedName, normalizedQuery := normalizeArtistName(a.Name), normalizeArtistName(name)
			switch {
			case strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name)):
				matches[0][a.ID] = true
			case normalizedName == normalizedQuery:
				matches[1][a.ID] = true
			case transliterate(normalizedName) == transliterate(normalizedQuery):
				matches[2][a.ID] = true
			case artistNameMatches(a.Name, name):
				matches[3][a.ID] = true
			}
		}

		if len(result.Response.Hits) < pageSize {
			break
		}
	}

	for _, ids := range matches {
		if len(ids) > 0 {
			return ids, nil
		}
	}

	return nil, nil
}

func artistNameMatches(name, query string) bool {
	name, query = normalizeArtistName(name), normalizeArtistName(query)
	return containsArtistWords(name, query) || containsArtistWords(transliterate(name), transliterate(query))
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

func transliterate(name string) string {
	letters := map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
		'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
		'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
		'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	}
	var out strings.Builder
	for _, r := range name {
		if replacement, ok := letters[r]; ok {
			out.WriteString(replacement)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func normalizeArtistName(name string) string {
	name = strings.TrimSpace(name)
	// Genius дописывает транслитерацию: «КИНО (KINO)» или «Король и Шут (Korol I Shut)».
	for i := strings.LastIndex(name, " ("); i >= 0 && strings.HasSuffix(name, ")"); i = strings.LastIndex(name, " (") {
		alias := strings.TrimSuffix(name[i+2:], ")")
		if alias != "" && (alias == strings.ToUpper(alias) || isLatinAlias(name[:i], alias)) {
			name = name[:i]
		} else {
			break
		}
	}

	name = norm.NFD.String(strings.ToLower(name))
	name = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}

		if r == '’' || r == '‘' || r == '`' {
			return '\''
		}

		return r
	}, name)

	return strings.Join(strings.Fields(name), " ")
}

func isLatinAlias(name, alias string) bool {
	if !strings.ContainsFunc(name, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) {
		return false
	}

	hasLatin := false
	for _, r := range alias {
		if unicode.IsLetter(r) {
			if !unicode.Is(unicode.Latin, r) {
				return false
			}
			hasLatin = true
		}
	}

	return hasLatin
}

// Catalog получает альбомы, полные треклисты и до TrackLimit популярных песен.
func (g *Genius) Catalog(ctx context.Context, lookup Lookup) (Catalog, error) {
	var c Catalog

	id, err := g.artistID(ctx, lookup)
	if err != nil {
		return c, err
	}

	var artist struct {
		Response struct {
			Artist struct {
				Name     string `json:"name"`
				ImageURL string `json:"image_url"`
			} `json:"artist"`
		} `json:"response"`
	}

	if err := g.api.get(ctx, fmt.Sprintf("/artists/%d", id), &artist); err != nil {
		return c, err
	}

	c.ArtistID, c.Name, c.LogoURL = id, artist.Response.Artist.Name, artist.Response.Artist.ImageURL

	c.Albums, err = g.albums(ctx, id)
	if err != nil {
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != http.StatusForbidden {
			return c, err
		}

		c.Warnings = append(c.Warnings,
			"веб-API Genius недоступен: альбомы извлечены из популярных треков, треклисты неполные")
	}

	for i := range c.Albums {
		if err := g.albumDetails(ctx, &c.Albums[i]); err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("альбом %d: %v", c.Albums[i].ID, err))
		}
	}

	c.Tracks, err = g.songs(ctx, id, lookup.TrackLimit)
	if err != nil {
		return c, err
	}

	if len(c.Albums) == 0 {
		seen := map[int64]bool{}
		for _, song := range c.Tracks {
			if song.Album != nil && song.Album.ID != 0 && song.Album.Name != "" && !seen[song.Album.ID] {
				c.Albums = append(c.Albums, *song.Album)
				seen[song.Album.ID] = true
			}
		}
	}

	return c, nil
}

func (g *Genius) albums(ctx context.Context, id int64) ([]Album, error) {
	r := []Album{}

	for page := 1; page > 0; {
		var response struct {
			Response struct {
				Albums   []Album `json:"albums"`
				NextPage *int    `json:"next_page"`
			} `json:"response"`
		}

		path := fmt.Sprintf("/artists/%d/albums?per_page=50&page=%d", id, page)
		if err := g.web.get(ctx, path, &response); err != nil {
			return nil, err
		}

		r = append(r, response.Response.Albums...)

		if response.Response.NextPage == nil || *response.Response.NextPage <= page {
			break
		}

		page = *response.Response.NextPage
	}

	return r, nil
}

func (g *Genius) albumDetails(ctx context.Context, album *Album) error {
	var response struct {
		Response struct {
			Album Album `json:"album"`
		} `json:"response"`
	}

	if err := g.web.get(ctx, fmt.Sprintf("/albums/%d", album.ID), &response); err != nil {
		return err
	}

	album.ReleaseDate = response.Response.Album.ReleaseDate
	if response.Response.Album.CoverURL != "" {
		album.CoverURL = response.Response.Album.CoverURL
	}

	return g.trackList(ctx, album)
}

func (g *Genius) trackList(ctx context.Context, album *Album) error {
	var text strings.Builder

	w := csv.NewWriter(&text)
	if err := w.Write([]string{"position", "title"}); err != nil {
		return err
	}

	for page := 1; page > 0; {
		var response struct {
			Response struct {
				Tracks []struct {
					Number int  `json:"number"`
					Song   Song `json:"song"`
				} `json:"tracks"`
				NextPage *int `json:"next_page"`
			} `json:"response"`
		}

		path := fmt.Sprintf("/albums/%d/tracks?per_page=50&page=%d", album.ID, page)
		if err := g.web.get(ctx, path, &response); err != nil {
			return err
		}

		for _, t := range response.Response.Tracks {
			if err := w.Write([]string{strconv.Itoa(t.Number), t.Song.Title}); err != nil {
				return err
			}
		}

		if response.Response.NextPage == nil || *response.Response.NextPage <= page {
			break
		}

		page = *response.Response.NextPage
	}

	w.Flush()

	if err := w.Error(); err != nil {
		return err
	}

	album.TrackList, album.Complete = text.String(), true

	return nil
}

func (g *Genius) songs(ctx context.Context, id int64, limit int) ([]Song, error) {
	r := []Song{}
	for page := 1; len(r) < limit; {
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
			var detail struct {
				Response struct {
					Song Song `json:"song"`
				} `json:"response"`
			}

			if err := g.api.get(ctx, fmt.Sprintf("/songs/%d", song.ID), &detail); err != nil {
				return nil, err
			}

			r = append(r, detail.Response.Song)
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
