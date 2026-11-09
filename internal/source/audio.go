package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"devkeeper.com/quiz/internal/domain"
)

const maxSourcePageBytes = 10 << 20

func readPage(ctx context.Context, client *http.Client, userAgent, address string) (_ string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{Base: req.URL.Host, Status: resp.StatusCode}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourcePageBytes+1))
	if err != nil {
		return "", err
	}

	if len(data) > maxSourcePageBytes {
		return "", errors.New("страница источника превышает 10 МБ")
	}

	return string(data), nil
}

// HitmoAudio ищет запись только после ручного запуска и сверяет ожидаемую длительность.
func HitmoAudio(ctx context.Context, s domain.Settings, search string, duration int) (string, error) {
	if duration <= 0 {
		return "", fmt.Errorf("%w: Hitmo требует ожидаемую длительность трека", domain.ErrInvalid)
	}

	address := strings.TrimRight(s.HitmoURL, "/") + "/search?" + url.Values{"q": {search}}.Encode()

	client := &http.Client{Timeout: time.Duration(s.HTTPTimeoutSeconds) * time.Second}

	page, err := readPage(ctx, client, s.UserAgent, address)
	if err != nil {
		return "", fmt.Errorf("поиск в Hitmo: %w", err)
	}

	return matchHitmoAudio(page, duration)
}

var (
	flightChunk = regexp.MustCompile(`(?s)self\.__next_f\.push\((\[[^<]*?\])\)</script>`)
	hitmoObject = regexp.MustCompile(`(?s)\{[^{}]*"download"\s*:\s*"https:[^{}]*\}`)
)

func matchHitmoAudio(page string, duration int) (string, error) {
	documents := []string{html.UnescapeString(page)}

	for _, match := range flightChunk.FindAllStringSubmatch(page, -1) {
		var data []json.RawMessage

		var payload string
		if json.Unmarshal([]byte(match[1]), &data) == nil && len(data) > 1 && json.Unmarshal(data[1], &payload) == nil {
			documents = append(documents, payload)
		}
	}
	// Старые страницы Hitmo вкладывают экранированный JSON прямо в HTML.
	documents = append(documents, strings.ReplaceAll(documents[0], `\"`, `"`))
	matches := map[string]bool{}

	for _, document := range documents {
		for _, object := range hitmoObject.FindAllString(document, -1) {
			var row struct {
				Download string          `json:"download"`
				Duration json.RawMessage `json:"duration"`
			}

			if json.Unmarshal([]byte(object), &row) != nil {
				continue
			}

			seconds, err := strconv.Atoi(strings.Trim(string(row.Duration), `"`))
			if err != nil || seconds <= 0 || seconds < duration-1 || seconds > duration+1 {
				continue
			}

			u, err := url.Parse(row.Download)
			if err == nil && domain.ValidateURL(row.Download) == nil && u.Scheme == "https" &&
				strings.HasSuffix(strings.ToLower(u.Path), ".mp3") {
				matches[row.Download] = true
			}
		}
	}

	return singleAudioURL(matches, "Hitmo: запись с подходящей длительностью")
}

func singleAudioURL(matches map[string]bool, description string) (string, error) {
	if len(matches) == 1 {
		for address := range matches {
			return address, nil
		}
	}

	if len(matches) > 1 {
		return "", fmt.Errorf("%w: %s найдена неоднозначно", domain.ErrInvalid, description)
	}

	return "", fmt.Errorf("%w: %s не найдена", domain.ErrNotFound, description)
}

// AudioSong загружает карточку Genius по сохранённому ID или точному совпадению песни и исполнителя.
func (g *Genius) AudioSong(ctx context.Context, track domain.Track, artist, album string) (Song, error) {
	id := track.GeniusID
	if id == nil {
		var search struct {
			Response struct {
				Hits []struct {
					Result Song `json:"result"`
				} `json:"hits"`
			} `json:"response"`
		}

		if err := g.api.get(ctx, "/search?"+url.Values{"q": {artist + " " + track.Name}}.Encode(), &search); err != nil {
			return Song{}, err
		}

		var matches []Song

		seen := map[int64]bool{}

		for _, hit := range search.Response.Hits {
			song := hit.Result
			if song.ID == 0 || seen[song.ID] || !artistNameMatches(song.PrimaryArtist.Name, artist) ||
				!strings.EqualFold(strings.TrimSpace(song.Title), strings.TrimSpace(track.Name)) {
				continue
			}

			seen[song.ID] = true

			detail, err := g.song(ctx, song.ID)
			if err != nil {
				return Song{}, err
			}

			matches = append(matches, detail)
		}

		if len(matches) > 1 && album != "" {
			matches = filterAlbum(matches, album)
		}

		if len(matches) != 1 {
			return Song{}, fmt.Errorf("%w: Genius не нашёл однозначное совпадение трека", domain.ErrNotFound)
		}

		return matches[0], nil
	}

	return g.song(ctx, *id)
}

func filterAlbum(songs []Song, name string) []Song {
	var result []Song

	for _, song := range songs {
		if song.Album != nil && strings.EqualFold(song.Album.Name, name) {
			result = append(result, song)
		}
	}

	return result
}

func (g *Genius) song(ctx context.Context, id int64) (Song, error) {
	var detail struct {
		Response struct {
			Song Song `json:"song"`
		} `json:"response"`
	}

	if err := g.api.get(ctx, fmt.Sprintf("/songs/%d", id), &detail); err != nil {
		return Song{}, err
	}

	if detail.Response.Song.ID == 0 {
		return Song{}, fmt.Errorf("%w: карточка Genius", domain.ErrNotFound)
	}

	return detail.Response.Song, nil
}

var (
	geniusPreview    = regexp.MustCompile(`(?i)"(?:previewUrl|preview_url|audio_preview_url)"\s*:\s*("(?:[^"\\]|\\.)+")`)
	embeddedPreviews = regexp.MustCompile(`(?i)"previews"\s*:\s*\[\s*\{[^{}]*?"url"\s*:\s*("(?:[^"\\]|\\.)+")`)
	applePlayer      = regexp.MustCompile(`(?i)<iframe\b[^>]*\bsrc=["'](https://embed\.music\.apple\.com/[^"']+)["']`)
	audioElement     = regexp.MustCompile(`(?is)<audio\b[^>]*(?:>.*?</audio\s*>|>)`)
	audioSrc         = regexp.MustCompile(`(?i)\bsrc=["'](https://[^"']+)["']`)
)

// Preview извлекает короткую запись из карточки Genius или её встроенного плеера.
func (g *Genius) Preview(ctx context.Context, song Song) (string, error) {
	if song.PreviewURL != "" {
		if err := validatePreviewURL(song.PreviewURL); err != nil {
			return "", err
		}

		return song.PreviewURL, nil
	}

	if err := domain.ValidateURL(song.URL); err != nil {
		return "", fmt.Errorf("%w: Genius не вернул ссылку на карточку", domain.ErrNotFound)
	}

	page, err := readPage(ctx, g.web.client, g.web.userAgent, song.URL)
	if err != nil {
		return "", err
	}

	matches := previewURLs(page)
	if len(matches) == 0 {
		players := applePlayer.FindAllStringSubmatch(page, 5)
		for _, player := range players {
			body, err := readPage(ctx, g.web.client, g.web.userAgent, html.UnescapeString(player[1]))
			if err != nil {
				return "", err
			}

			for address := range previewURLs(body) {
				matches[address] = true
			}
		}
	}

	return singleAudioURL(matches, "Genius: ссылка на короткую запись")
}

func previewURLs(page string) map[string]bool {
	page = html.UnescapeString(page)
	matches := map[string]bool{}

	for _, body := range []string{page, strings.ReplaceAll(page, `\"`, `"`)} {
		for _, match := range append(geniusPreview.FindAllStringSubmatch(body, -1),
			embeddedPreviews.FindAllStringSubmatch(body, -1)...) {
			var address string
			if json.Unmarshal([]byte(match[1]), &address) == nil && validatePreviewURL(address) == nil {
				matches[address] = true
			}
		}
	}

	for _, audio := range audioElement.FindAllString(page, -1) {
		for _, match := range audioSrc.FindAllStringSubmatch(audio, -1) {
			if validatePreviewURL(match[1]) == nil {
				matches[match[1]] = true
			}
		}
	}

	return matches
}

func validatePreviewURL(address string) error {
	if err := domain.ValidateURL(address); err != nil {
		return err
	}

	if !strings.HasPrefix(address, "https://") {
		return fmt.Errorf("%w: превью требует HTTPS", domain.ErrInvalid)
	}

	return nil
}
