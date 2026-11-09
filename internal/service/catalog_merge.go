package service

import (
	"fmt"
	"strings"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

func mergeCatalog(current *source.Catalog, next source.Catalog) {
	if current.ArtistID == 0 {
		current.ArtistID = next.ArtistID
	}

	current.YTMusicID = firstKnown(current.YTMusicID, next.YTMusicID)
	current.YtSubs = firstKnown(current.YtSubs, next.YtSubs)
	current.YtPlays = firstKnown(current.YtPlays, next.YtPlays)
	current.Name = firstKnown(current.Name, next.Name)

	if next.LogoURL != "" && domain.ValidateURL(next.LogoURL) != nil {
		current.Warnings = append(current.Warnings, "источник вернул некорректный адрес логотипа")
	} else {
		current.LogoURL = firstKnown(current.LogoURL, next.LogoURL)
	}

	current.Warnings = append(current.Warnings, next.Warnings...)

	for _, album := range next.Albums {
		if album.Name != "" {
			mergeAlbum(current, album)
		}
	}

	for _, track := range next.Tracks {
		if track.Title != "" {
			mergeTrack(current, track)
		}
	}
}

func mergeAlbum(c *source.Catalog, album source.Album) {
	if album.CoverURL != "" && domain.ValidateURL(album.CoverURL) != nil {
		c.Warnings = append(c.Warnings, fmt.Sprintf("альбом %q: некорректный адрес обложки", album.Name))
		album.CoverURL = ""
	}

	i, ambiguous := albumMatch(c.Albums, album)
	if ambiguous {
		c.Warnings = append(c.Warnings, fmt.Sprintf("альбом %q: неоднозначное совпадение источников", album.Name))
		return
	}

	if i < 0 {
		c.Albums = append(c.Albums, album)
		return
	}

	old := &c.Albums[i]
	old.ID = firstKnown(old.ID, album.ID)
	old.YTMusicID = firstKnown(old.YTMusicID, album.YTMusicID)
	old.MusicBrainzID = firstKnown(old.MusicBrainzID, album.MusicBrainzID)
	old.CoverURL = firstKnown(old.CoverURL, album.CoverURL)
	old.ReleaseDate = firstKnown(old.ReleaseDate, album.ReleaseDate)
	old.YtPlays = firstKnown(old.YtPlays, album.YtPlays)

	if old.TrackList == "" || !old.Complete && album.Complete {
		old.TrackList, old.Complete = album.TrackList, album.Complete
	}
}

func mergeTrack(c *source.Catalog, track source.Song) {
	i, ambiguous := trackMatch(c.Tracks, track)
	if ambiguous {
		c.Warnings = append(c.Warnings, fmt.Sprintf("трек %q: неоднозначное совпадение источников", track.Title))
		return
	}

	if i < 0 {
		c.Tracks = append(c.Tracks, track)
		return
	}

	old := &c.Tracks[i]
	old.ID = firstKnown(old.ID, track.ID)
	old.YTMusicID = firstKnown(old.YTMusicID, track.YTMusicID)
	old.MusicBrainzID = firstKnown(old.MusicBrainzID, track.MusicBrainzID)
	old.YtPlays = firstKnown(old.YtPlays, track.YtPlays)
	old.Duration = firstKnown(old.Duration, track.Duration)
	old.Album = firstKnown(old.Album, track.Album)
}

func albumMatch(albums []source.Album, album source.Album) (int, bool) {
	return bestMatch(albums, func(other source.Album) (bool, bool) {
		ids := catalogSourceIDs{album.YTMusicID, album.ID, album.MusicBrainzID}
		otherIDs := catalogSourceIDs{other.YTMusicID, other.ID, other.MusicBrainzID}
		exact := ids.matches(otherIDs)
		distinct := ids.conflicts(otherIDs)

		return exact, album.Name != "" && sameTitle(album.Name, other.Name) && !distinct
	})
}

func bestMatch[T any](items []T, check func(T) (bool, bool)) (int, bool) {
	exact, similar := -1, -1

	for i, item := range items {
		id, title := check(item)
		if id {
			if exact >= 0 {
				return -1, true
			}

			exact = i
		}

		if title {
			if similar == -1 {
				similar = i
			} else {
				similar = -2
			}
		}
	}

	if exact >= 0 {
		return exact, false
	}

	return similar, similar == -2
}

type catalogSourceIDs struct {
	yt     *string
	genius int64
	mb     string
}

func (ids catalogSourceIDs) matches(other catalogSourceIDs) bool {
	return (ids.yt != nil && other.yt != nil && *ids.yt == *other.yt) ||
		(ids.genius != 0 && ids.genius == other.genius) || (ids.mb != "" && ids.mb == other.mb)
}

func (ids catalogSourceIDs) conflicts(other catalogSourceIDs) bool {
	return (ids.yt != nil && other.yt != nil && *ids.yt != *other.yt) ||
		(ids.genius != 0 && other.genius != 0 && ids.genius != other.genius) ||
		(ids.mb != "" && other.mb != "" && ids.mb != other.mb)
}

func sameTitle(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

func firstKnown[T comparable](a, b T) T {
	var zero T
	if a == zero {
		return b
	}

	return a
}

func trackMatch(tracks []source.Song, track source.Song) (int, bool) {
	return bestMatch(tracks, func(other source.Song) (bool, bool) {
		ids := catalogSourceIDs{track.YTMusicID, track.ID, track.MusicBrainzID}
		otherIDs := catalogSourceIDs{other.YTMusicID, other.ID, other.MusicBrainzID}
		exact := ids.matches(otherIDs)
		title := sameTitle(track.Title, other.Title)
		album := track.Album == nil || other.Album == nil || track.Album.Name == "" || other.Album.Name == "" ||
			sameTitle(track.Album.Name, other.Album.Name)
		duration := track.Duration == nil || other.Duration == nil ||
			(*track.Duration <= *other.Duration+1 && *other.Duration <= *track.Duration+1)
		distinct := ids.conflicts(otherIDs)

		return exact, title && album && duration && !distinct
	})
}
