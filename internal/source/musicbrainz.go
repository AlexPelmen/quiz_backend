package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"devkeeper.com/quiz/internal/domain"
)

// MusicBrainz дополняет каталог структурированными данными исполнителей.
type MusicBrainz struct {
	api    jsonClient
	covers jsonClient
}

// NewMusicBrainz создаёт клиент с соблюдением лимита не более одного запроса в секунду.
func NewMusicBrainz(s domain.Settings) *MusicBrainz {
	return &MusicBrainz{api: jsonClient{
		client: &http.Client{Timeout: time.Duration(s.HTTPTimeoutSeconds) * time.Second},
		base:   s.MusicBrainzURL, userAgent: s.UserAgent, interval: 1100 * time.Millisecond,
		retryStatus: http.StatusServiceUnavailable, retries: min(3, s.Source429Retries),
		retryDelay:    time.Duration(s.Source429DelaySeconds) * time.Second,
		maxRetryDelay: time.Duration(s.Source429MaxDelaySeconds) * time.Second,
	}, covers: jsonClient{
		client: &http.Client{Timeout: time.Duration(s.HTTPTimeoutSeconds) * time.Second},
		base:   s.CoverArtArchiveURL, userAgent: s.UserAgent,
	}}
}

type mbArtist struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Country   string `json:"country"`
	Relations []struct {
		Type      string `json:"type"`
		Direction string `json:"direction"`
		Artist    *struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"relations"`
}

// Enrich выбирает только единственное точное совпадение имени; неоднозначность не маскируется.
func (m *MusicBrainz) Enrich(ctx context.Context, name string) (Enrichment, error) {
	id, err := m.FindArtist(ctx, name)
	if err != nil {
		return Enrichment{}, err
	}
	if id == "" {
		return Enrichment{}, fmt.Errorf("MusicBrainz: имя %q не найдено однозначно", name)
	}
	return m.EnrichByID(ctx, id)
}

// FindArtist ищет ID одним запросом и возвращает пустой ID при отсутствии однозначного совпадения.
func (m *MusicBrainz) FindArtist(ctx context.Context, name string) (string, error) {
	var result struct {
		Artists []mbArtist `json:"artists"`
	}
	// Экранирование специальных символов Lucene сохраняет имя как буквальный поисковый запрос.
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)

	path := "/artist?fmt=json&limit=100&query=" + url.QueryEscape(`artist:"`+replacer.Replace(name)+`"`)
	if err := m.api.get(ctx, path, &result); err != nil {
		return "", err
	}

	matches := []mbArtist{}

	for _, a := range result.Artists {
		if strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name)) {
			matches = append(matches, a)
		}
	}

	if len(matches) != 1 {
		return "", nil
	}

	return matches[0].ID, nil
}

// EnrichByID использует ранее сопоставленного исполнителя.
func (m *MusicBrainz) EnrichByID(ctx context.Context, id string) (Enrichment, error) {
	var a mbArtist
	if err := m.api.get(ctx, "/artist/"+url.PathEscape(id)+"?fmt=json&inc=artist-rels", &a); err != nil {
		return Enrichment{}, err
	}

	r := Enrichment{ID: a.ID, FullName: a.Name, Country: a.Country, Members: []string{}}

	for _, relation := range a.Relations {
		if relation.Type == "member of band" && relation.Direction == "backward" && relation.Artist != nil {
			r.Members = append(r.Members, relation.Artist.Name)
		}
	}

	r.MembersKnown = a.Type == "Person" || len(r.Members) > 0

	return r, nil
}

// Albums получает одним запросом типы и даты релизов без треклистов.
func (m *MusicBrainz) Albums(ctx context.Context, id string) ([]Album, error) {
	var response struct {
		Groups []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Date      string   `json:"first-release-date"`
			Type      string   `json:"primary-type"`
			Secondary []string `json:"secondary-types"`
		} `json:"release-groups"`
	}

	params := url.Values{
		"artist": {id}, "type": {"album"}, "limit": {"100"}, "fmt": {"json"},
		"release-group-status": {"website-default"},
	}
	if err := m.api.get(ctx, "/release-group?"+params.Encode(), &response); err != nil {
		return nil, err
	}

	albums := make([]Album, 0, len(response.Groups))

	for _, group := range response.Groups {
		if group.ID == "" || group.Title == "" {
			continue
		}

		kind := group.Type
		if len(group.Secondary) > 0 {
			kind = group.Secondary[0]
		}

		albums = append(albums, Album{Name: group.Title, Type: kind, MusicBrainzID: group.ID, ReleaseDate: group.Date})
	}

	return albums, nil
}

// AlbumCovers ищет обложки только для выбранных альбомов без ссылки.
func (m *MusicBrainz) AlbumCovers(ctx context.Context, albums []Album) error {
	var issues error

	for i := range albums {
		album := &albums[i]
		if album.HasCover || album.CoverURL != "" || album.MusicBrainzID == "" {
			continue
		}

		cover, err := m.cover(ctx, album.MusicBrainzID)
		if err != nil {
			issues = errors.Join(issues, fmt.Errorf("обложка %q: %w", album.Name, err))

			if ctx.Err() != nil {
				break
			}

			continue
		}

		album.CoverURL = cover
	}

	return issues
}

type mbRecording struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Length int    `json:"length"`
	Video  bool   `json:"video"`
}

type mbRecordings struct {
	Recordings []mbRecording `json:"recordings"`
}

// Tracks заполняет длительность отдельных треков и добирает записи при нехватке.
func (m *MusicBrainz) Tracks(ctx context.Context, artistID string, tracks []Song, limit int) ([]Song, error) {
	var issues error

	for i := range tracks {
		if tracks[i].Duration != nil {
			continue
		}

		if err := m.fillDuration(ctx, artistID, &tracks[i]); err != nil {
			issues = errors.Join(issues, err)
		}

		if ctx.Err() != nil {
			return tracks, issues
		}
	}

	if len(tracks) >= limit {
		return tracks, issues
	}

	found, err := m.ListRecordings(ctx, artistID)
	if err != nil {
		return tracks, errors.Join(issues, err)
	}
	for _, song := range found {
		if slices.ContainsFunc(tracks, func(old Song) bool { return sameSongTitle(old, song) }) {
			continue
		}
		tracks = append(tracks, song)
		if len(tracks) >= limit {
			break
		}
	}
	return tracks, issues
}

// ListRecordings запрашивает первую страницу записей исполнителя.
func (m *MusicBrainz) ListRecordings(ctx context.Context, artistID string) ([]Song, error) {
	var data mbRecordings
	params := url.Values{"artist": {artistID}, "limit": {"25"}, "fmt": {"json"}}
	if err := m.api.get(ctx, "/recording?"+params.Encode(), &data); err != nil {
		return nil, err
	}

	var songs []Song
	for _, row := range data.Recordings {
		if row.ID == "" || row.Title == "" || row.Video {
			continue
		}

		songs = append(songs, Song{Title: row.Title, MusicBrainzID: row.ID, Duration: recordingDuration(row.Length)})
	}
	return songs, nil
}

// Recording дополняет запись результатом одного запроса.
func (m *MusicBrainz) Recording(ctx context.Context, artistID string, song Song) (Song, error) {
	err := m.fillDuration(ctx, artistID, &song)
	return song, err
}

// AlbumCover ищет обложку по ID группы релизов.
func (m *MusicBrainz) AlbumCover(ctx context.Context, id string) (string, error) {
	return m.cover(ctx, id)
}

// UseParserRetries передаёт управление повторами парсеру.
func (m *MusicBrainz) UseParserRetries() { m.api.retries = 0 }

func (m *MusicBrainz) fillDuration(ctx context.Context, id string, song *Song) error {
	if song.MusicBrainzID != "" {
		var row mbRecording
		if err := m.api.get(ctx, "/recording/"+url.PathEscape(song.MusicBrainzID)+"?fmt=json", &row); err != nil {
			return err
		}

		song.Duration = recordingDuration(row.Length)

		return nil
	}

	var data mbRecordings

	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(song.Title)
	query := `arid:` + id + ` AND recording:"` + quoted + `"`

	params := url.Values{"query": {query}, "limit": {"25"}, "fmt": {"json"}}
	if err := m.api.get(ctx, "/recording?"+params.Encode(), &data); err != nil {
		return err
	}

	matches := slices.DeleteFunc(data.Recordings, func(row mbRecording) bool {
		return row.Video || row.ID == "" || !strings.EqualFold(strings.TrimSpace(row.Title), strings.TrimSpace(song.Title))
	})
	if len(matches) == 1 {
		song.MusicBrainzID = matches[0].ID
		song.Duration = recordingDuration(matches[0].Length)
	}

	return nil
}

func recordingDuration(ms int) *int {
	if ms < 500 {
		return nil
	}

	seconds := (ms + 500) / 1000

	return &seconds
}

func (m *MusicBrainz) cover(ctx context.Context, id string) (string, error) {
	var data struct {
		Images []struct {
			Image    string `json:"image"`
			Front    bool   `json:"front"`
			Approved bool   `json:"approved"`
		} `json:"images"`
	}

	err := m.covers.get(ctx, "/release-group/"+url.PathEscape(id), &data)

	var httpErr *HTTPError

	if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	for _, image := range data.Images {
		if image.Front && image.Approved && domain.ValidateURL(image.Image) == nil {
			return image.Image, nil
		}
	}

	return "", nil
}
