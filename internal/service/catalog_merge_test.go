package service

import (
	"testing"

	"devkeeper.com/quiz/internal/source"
)

func TestCatalogPriorityAndTrackVersions(t *testing.T) {
	const logo = "https://example.com/yt.jpg"

	video, albumID := "video", "album"
	seconds, otherDuration := 180, 210
	plays := int64(3_000_000)
	ytAlbum := source.Album{Name: "Album", YTMusicID: &albumID, ReleaseDate: "2020"}

	var catalog source.Catalog

	mergeCatalog(&catalog, source.Catalog{
		Name: "Artist", LogoURL: logo, Albums: []source.Album{ytAlbum},
		Tracks: []source.Song{{Title: "Song", Album: &ytAlbum, YTMusicID: &video, YtPlays: &plays, Duration: &seconds}},
	})

	geniusAlbum := source.Album{Name: "album", ID: 2, CoverURL: "https://example.com/g.jpg", ReleaseDate: "2021"}
	mergeCatalog(&catalog, source.Catalog{
		ArtistID: 1, LogoURL: "https://example.com/genius.jpg", Albums: []source.Album{geniusAlbum},
		Tracks: []source.Song{{Title: "song", Album: &geniusAlbum, ID: 3}},
	})
	mergeCatalog(&catalog, source.Catalog{Tracks: []source.Song{
		{Title: "Song", Album: &ytAlbum, MusicBrainzID: "original", Duration: &seconds},
		{Title: "Song", Album: &ytAlbum, MusicBrainzID: "other-version", Duration: &otherDuration},
	}})

	if catalog.LogoURL != logo || len(catalog.Albums) != 1 || catalog.Albums[0].ReleaseDate != "2020" ||
		catalog.Albums[0].CoverURL != geniusAlbum.CoverURL {
		t.Fatalf("потерян приоритет альбома или исполнителя: %+v", catalog)
	}

	if len(catalog.Tracks) != 2 || catalog.Tracks[0].ID != 3 || catalog.Tracks[0].MusicBrainzID != "original" ||
		catalog.Tracks[0].YtPlays != &plays || catalog.Tracks[1].MusicBrainzID != "other-version" {
		t.Fatalf("потеряны метрики, ID или отдельная версия: %+v", catalog.Tracks)
	}
}
