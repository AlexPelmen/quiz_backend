package source

import "context"

// CatalogSource позволяет заменить поставщика каталога без изменения парсера.
type CatalogSource interface {
	Catalog(context.Context, Lookup) (Catalog, error)
}

// Lookup задаёт исполнителя и лимиты импорта.
type Lookup struct {
	Name         string
	GeniusID     *int64
	TrackLimit   int
	AlbumLimit   int
	Base         Catalog
	NeedArtist   bool
	StudioGroups []Album
}

// Catalog содержит подготовленные сведения источника.
type Catalog struct {
	ArtistCandidates  []string        `json:"artist_candidates,omitempty"`
	ArtistUnavailable bool            `json:"artist_unavailable,omitempty"`
	Failure           *RequestFailure `json:"failure,omitempty"`
	AlbumBrowseID     string          `json:"album_browse_id,omitempty"`
	AlbumParams       string          `json:"album_params,omitempty"`
	AlbumsRanked      bool            `json:"albums_ranked"`
	RankedAlbumIDs    []string        `json:"-"`
	TopSongs          []Song          `json:"top_songs"`
	TopSongsPlaylist  string          `json:"top_songs_playlist"`
	ArtistID          int64           `json:"genius_id"`
	YTMusicID         *string         `json:"yt_music_id"`
	YtPlays           *int64          `json:"yt_plays"`
	YtSubs            *int64          `json:"yt_subs"`
	Name              string          `json:"name"`
	LogoURL           string          `json:"logo_url"`
	Albums            []Album         `json:"albums"`
	Tracks            []Song          `json:"tracks"`
	Warnings          []string        `json:"warnings"`
}

// RequestFailure содержит классификацию ошибки отдельного запроса YouTube Music.
type RequestFailure struct {
	Kind   string `json:"kind"`
	Status int    `json:"status"`
}

// Error описывает ошибку без данных карточки и URL.
func (e *RequestFailure) Error() string {
	if e.Status != 0 {
		return (&HTTPError{Base: "YouTube Music", Status: e.Status}).Error()
	}
	return "YouTube Music: " + e.Kind
}

// Album содержит метаданные релиза и необязательный сохранённый треклист.
type Album struct {
	HasCover      bool    `json:"-"`
	SkipPlays     bool    `json:"-"`
	Type          string  `json:"type"`
	YTMusicID     *string `json:"yt_music_id"`
	MusicBrainzID string  `json:"music_brainz_id"`
	YtPlays       *int64  `json:"yt_plays"`
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	CoverURL      string  `json:"cover_art_url"`
	ReleaseDate   string  `json:"release_date"`
	TrackList     string  `json:"track_list"`
	Complete      bool    `json:"complete"`
}

// Song описывает сведения песни и доступные источники записи.
type Song struct {
	YTMusicID     *string `json:"yt_music_id"`
	MusicBrainzID string  `json:"music_brainz_id"`
	YtPlays       *int64  `json:"yt_plays"`
	URL           string  `json:"url"`
	PreviewURL    string  `json:"preview_url"`
	Duration      *int    `json:"duration_seconds"`
	PrimaryArtist struct {
		Name string `json:"name"`
	} `json:"primary_artist"`

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
	ID           string
	FullName     string
	Country      string
	Members      []string
	MembersKnown bool
}
