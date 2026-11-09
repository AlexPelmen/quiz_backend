package source

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"devkeeper.com/quiz/internal/domain"
)

//go:embed ytmusic.py
var ytMusicScript string

type ytMusicLookup struct {
	Name             string  `json:"name"`
	ArtistID         *string `json:"artist_id"`
	Subs             *int64  `json:"yt_subs"`
	KeepSubs         bool    `json:"keep_subs"`
	SavedTracks      []Song  `json:"saved_tracks,omitempty"`
	Tracks           []Song  `json:"tracks,omitempty"`
	Albums           []Album `json:"albums,omitempty"`
	TopSongs         []Song  `json:"top_songs,omitempty"`
	TopSongsPlaylist string  `json:"top_songs_playlist,omitempty"`
}

// YTMusicCatalog получает метаданные через ytmusicapi без скачивания файлов.
func YTMusicCatalog(ctx context.Context, s domain.Settings, artist domain.Artist, saved []Song) (Catalog, error) {
	return runYTMusic(ctx, s, ytMusicLookup{
		Name: artist.Name, ArtistID: artist.YTMusicID, Subs: artist.YtSubs,
		KeepSubs: artist.YtSubs != nil || slices.Contains(artist.ManualFields, "yt_subs"), SavedTracks: saved,
	})
}

// YTMusicTracks сопоставляет треки из резервных источников с YouTube Music.
func YTMusicTracks(ctx context.Context, s domain.Settings, artist domain.Artist, tracks []Song) (Catalog, error) {
	return runYTMusic(ctx, s, ytMusicLookup{Name: artist.Name, ArtistID: artist.YTMusicID, Tracks: tracks})
}

// YTMusicAlbumPlays получает прослушивания выбранных релизов через их популярные треки.
func YTMusicAlbumPlays(ctx context.Context, s domain.Settings, catalog Catalog) (Catalog, error) {
	return runYTMusic(ctx, s, ytMusicLookup{
		Albums: catalog.Albums, TopSongs: catalog.TopSongs, TopSongsPlaylist: catalog.TopSongsPlaylist,
	})
}

func runYTMusic(ctx context.Context, s domain.Settings, lookup ytMusicLookup) (Catalog, error) {
	input, err := json.Marshal(lookup)
	if err != nil {
		return Catalog{}, err
	}

	cmd := exec.CommandContext(ctx, s.YTMusicPython, "-X", "utf8", "-c", ytMusicScript)
	cmd.Stdin = bytes.NewReader(input)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	output, err := cmd.Output()

	if ctx.Err() != nil {
		return Catalog{}, ctx.Err()
	}

	if err != nil {
		return Catalog{}, fmt.Errorf("YouTube Music: %w: %.512s", err, strings.TrimSpace(stderr.String()))
	}

	var catalog Catalog
	if err := json.Unmarshal(output, &catalog); err != nil {
		return catalog, fmt.Errorf("ответ YouTube Music: %w", err)
	}

	return catalog, nil
}
