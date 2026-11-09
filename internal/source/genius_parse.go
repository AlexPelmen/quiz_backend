package source

import (
	"context"
	"fmt"
	"slices"

	"devkeeper.com/quiz/internal/domain"
)

// UseParserRetries передаёт управление повторами парсеру.
func (g *Genius) UseParserRetries() { g.api.retries, g.web.retries = 0, 0 }

// GeniusArtistSearch собирает совпадения нескольких страниц одного запроса поиска.
type GeniusArtistSearch struct {
	query, name string
	matches     [5]map[int64]bool
}

func newGeniusArtistSearch(query, name string) *GeniusArtistSearch {
	return &GeniusArtistSearch{query: query, name: name, matches: [5]map[int64]bool{{}, {}, {}, {}, {}}}
}

// GeniusArtistSearches подготавливает варианты имени для поиска.
func GeniusArtistSearches(name string) []*GeniusArtistSearch {
	var searches []*GeniusArtistSearch
	var seen []string
	for _, query := range []string{name, domain.TransliterateRussian(normalizeArtistName(name)), compactArtistName(normalizeArtistName(name))} {
		if query != "" && !slices.Contains(seen, query) {
			seen = append(seen, query)
			searches = append(searches, newGeniusArtistSearch(query, name))
		}
	}
	return searches
}

// Result возвращает ID и признак, что найдено хотя бы одно совпадение.
func (s *GeniusArtistSearch) Result() (int64, bool) {
	for _, ids := range s.matches {
		if len(ids) > 1 {
			return 0, true
		}
		for id := range ids {
			return id, true
		}
	}
	return 0, false
}

// Artist запрашивает карточку по известному ID.
func (g *Genius) Artist(ctx context.Context, id int64) (Catalog, error) {
	var response struct {
		Response struct {
			Artist struct {
				Name  string `json:"name"`
				Image string `json:"image_url"`
			} `json:"artist"`
		} `json:"response"`
	}
	err := g.api.get(ctx, fmt.Sprintf("/artists/%d", id), &response)
	artist := response.Response.Artist
	return Catalog{Name: artist.Name, LogoURL: artist.Image}, err
}

// Albums запрашивает страницу альбомов.
func (g *Genius) Albums(ctx context.Context, id int64) ([]Album, error) {
	var response struct {
		Response struct {
			Albums []Album `json:"albums"`
		} `json:"response"`
	}
	err := g.web.get(ctx, fmt.Sprintf("/artists/%d/albums?per_page=20&page=1", id), &response)
	return response.Response.Albums, err
}

// Album запрашивает карточку альбома.
func (g *Genius) Album(ctx context.Context, album Album) (Album, error) {
	err := g.albumDetails(ctx, &album)
	return album, err
}

// Songs запрашивает одну страницу популярных треков.
func (g *Genius) Songs(ctx context.Context, id int64, page int) ([]Song, bool, error) {
	var response struct {
		Response struct {
			Songs    []Song `json:"songs"`
			NextPage *int   `json:"next_page"`
		} `json:"response"`
	}
	err := g.api.get(ctx, fmt.Sprintf("/artists/%d/songs?sort=popularity&per_page=50&page=%d", id, page), &response)
	return response.Response.Songs, response.Response.NextPage != nil && *response.Response.NextPage > page, err
}
