package source

import (
	"regexp"
	"slices"
	"strings"
)

// StudioAlbumLimit ограничивает выборку альбомов одного исполнителя.
const StudioAlbumLimit = 5

var (
	editionTitle = regexp.MustCompile(
		"(?i)(deluxe|expanded|anniversary|bonus tracks?|special edition|remaster)",
	)
	liveTitle = regexp.MustCompile(
		`(?i)(^live (at |in |from |on )|[(\[]live([ )\]]|$)|[-–—] *live($| )|^unplugged$|^live$|^концерт )`,
	)
	compilationTitle = regexp.MustCompile(
		"(?i)(^the best of |^best of |greatest hits|сборник|лучшие песни|лучшие хиты)",
	)
)

// IsStudioAlbum отбирает обычные издания по типу и обозначениям в названии.
func IsStudioAlbum(a Album) bool {
	name := strings.TrimSpace(a.Name)
	return name != "" && (a.Type == "" || strings.EqualFold(a.Type, "Album")) &&
		!editionTitle.MatchString(name) && !liveTitle.MatchString(name) && !compilationTitle.MatchString(name)
}

// SameAlbum сопоставляет релизы по внешним ID или нормализованному названию.
func SameAlbum(a, b Album) bool {
	if a.YTMusicID != nil && b.YTMusicID != nil {
		return *a.YTMusicID == *b.YTMusicID
	}

	if a.ID != 0 && b.ID != 0 {
		return a.ID == b.ID
	}

	if a.MusicBrainzID != "" && b.MusicBrainzID != "" {
		return a.MusicBrainzID == b.MusicBrainzID
	}

	name := strings.Join(strings.Fields(a.Name), " ")

	return name != "" && strings.EqualFold(name, strings.Join(strings.Fields(b.Name), " "))
}

// NeedsAlbums сообщает, нужны ли дополнительные альбомы, даты или ссылки на обложки.
func NeedsAlbums(albums []Album) bool {
	return len(albums) < StudioAlbumLimit || slices.ContainsFunc(albums, missingAlbumFields)
}

func missingAlbumFields(a Album) bool {
	return (!a.HasCover && a.CoverURL == "") || a.ReleaseDate == ""
}

func sameSongTitle(a, b Song) bool {
	return (a.ID != 0 && a.ID == b.ID) || strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(b.Title))
}
