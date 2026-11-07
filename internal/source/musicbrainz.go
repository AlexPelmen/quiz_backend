package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"devkeeper.com/quiz/internal/domain"
)

// MusicBrainz дополняет каталог структурированными данными исполнителей.
type MusicBrainz struct{ api jsonClient }

// NewMusicBrainz создаёт клиент с соблюдением лимита не более одного запроса в секунду.
func NewMusicBrainz(s domain.Settings) *MusicBrainz {
	return &MusicBrainz{api: jsonClient{
		client: &http.Client{Timeout: time.Duration(s.HTTPTimeoutSeconds) * time.Second},
		base:   s.MusicBrainzURL, userAgent: s.UserAgent, interval: 1100 * time.Millisecond,
		retries: s.Source429Retries, retryDelay: time.Duration(s.Source429DelaySeconds) * time.Second,
		maxRetryDelay: time.Duration(s.Source429MaxDelaySeconds) * time.Second,
	}}
}

type mbArtist struct {
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
	var result struct {
		Artists []mbArtist `json:"artists"`
	}
	// Экранирование специальных символов Lucene сохраняет имя как буквальный поисковый запрос.
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)

	path := "/artist?fmt=json&limit=100&query=" + url.QueryEscape(`artist:"`+replacer.Replace(name)+`"`)
	if err := m.api.get(ctx, path, &result); err != nil {
		return Enrichment{}, err
	}

	matches := []mbArtist{}

	for _, a := range result.Artists {
		if strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name)) {
			matches = append(matches, a)
		}
	}

	if len(matches) != 1 {
		return Enrichment{}, fmt.Errorf("MusicBrainz: имя %q не найдено однозначно", name)
	}

	var a mbArtist
	if err := m.api.get(ctx, "/artist/"+matches[0].ID+"?fmt=json&inc=artist-rels", &a); err != nil {
		return Enrichment{}, err
	}

	r := Enrichment{ID: a.ID, FullName: a.Name, Country: a.Country, Members: []string{}}

	for _, relation := range a.Relations {
		if relation.Type == "member of band" && relation.Direction == "backward" && relation.Artist != nil {
			r.Members = append(r.Members, relation.Artist.Name)
		}
	}

	return r, nil
}
