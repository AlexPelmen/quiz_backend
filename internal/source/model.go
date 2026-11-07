package source

import "context"

// CatalogSource позволяет заменить поставщика каталога без изменения парсера.
type CatalogSource interface {
	Catalog(context.Context, Lookup) (Catalog, error)
}

// Lookup задаёт исполнителя и лимит отдельных треков.
type Lookup struct {
	Name       string
	GeniusID   *int64
	TrackLimit int
}

// Catalog содержит подготовленные сведения источника.
type Catalog struct {
	ArtistID int64
	Name     string
	LogoURL  string
	Albums   []Album
	Tracks   []Song
	Warnings []string
}

// Album содержит полный CSV-треклист и метаданные альбома Genius.
type Album struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CoverURL    string `json:"cover_art_url"`
	ReleaseDate string `json:"release_date"`
	TrackList   string
	Complete    bool
}

// Song описывает сведения песни и доступные источники записи.
type Song struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Album      *Album `json:"album"`
	YouTubeURL string `json:"youtube_url"`
	Media      []struct {
		Provider string `json:"provider"`
		Type     string `json:"type"`
		URL      string `json:"url"`
	} `json:"media"`
}

// Enrichment содержит необязательные структурированные сведения MusicBrainz.
type Enrichment struct {
	ID       string
	FullName string
	Country  string
	Members  []string
}
