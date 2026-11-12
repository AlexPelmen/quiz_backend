package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

func (a *App) collectCatalog(
	ctx context.Context, artist *domain.Artist, deps parseDependencies, saved source.Catalog,
) source.Catalog {
	timeout := time.Duration(deps.Settings.TaskTimeoutSeconds) * time.Second
	catalog := readYouTubeMusic(ctx, *artist, deps, saved.Tracks)

	limit := importTrackLimit(*artist, catalog.YtSubs)
	albumLimit := domain.AlbumImportLimit(firstKnown(artist.YtSubs, catalog.YtSubs))
	catalog.Tracks = slices.DeleteFunc(catalog.Tracks, func(track source.Song) bool {
		return slices.ContainsFunc(saved.Tracks, func(existing source.Song) bool {
			return trackTitleKey(track.Title) != "" && trackTitleKey(track.Title) == trackTitleKey(existing.Title) && track.Title != existing.Title
		})
	})
	catalog.Tracks = uniqueTrackTitles(catalog.Tracks)
	catalog.Tracks = catalog.Tracks[:min(limit, len(catalog.Tracks))]
	reuseSaved(&catalog, saved)

	for _, track := range saved.Tracks {
		if len(catalog.Tracks) >= limit {
			break
		}

		if index, ambiguous := trackMatch(catalog.Tracks, track); index < 0 && !ambiguous && !slices.ContainsFunc(catalog.Tracks, func(t source.Song) bool {
			return trackTitleKey(t.Title) != "" && trackTitleKey(t.Title) == trackTitleKey(track.Title)
		}) {
			catalog.Tracks = append(catalog.Tracks, track)
		}
	}

	ranked := slices.Clone(catalog.Albums)

	for _, album := range saved.Albums {
		if album.YTMusicID != nil {
			if index, ambiguous := albumMatch(catalog.Albums, album); index < 0 && !ambiguous {
				catalog.Albums = append(catalog.Albums, album)
			}
		}
	}

	needArtist := catalog.Name == "" && artist.FullName == "" && !slices.Contains(artist.ManualFields, "full_name") ||
		catalog.LogoURL == "" && (artist.LogoDownloadID == nil || slices.Contains(artist.MissingFields, "logo"))

	var groups []source.Album

	var musicBrainzName string

	if deps.Enrichment != nil {
		sourceCtx, stop := context.WithTimeout(ctx, timeout/7)

		deps.progress("MusicBrainz", "Проверяем исполнителя и типы релизов")

		started := time.Now()
		groups, musicBrainzName = readMusicBrainz(sourceCtx, deps.Enrichment, artist, &catalog)

		deps.sourceDone("MusicBrainz", started, nil)

		stop()
	}

	catalog.Albums = pickStudioAlbums(catalog.Albums, groups, albumLimit)
	if catalog.AlbumsRanked {
		catalog.RankedAlbumIDs = rankedAlbumIDs(ranked, groups, albumLimit)
	}

	if needArtist || len(catalog.Tracks) < limit || source.NeedsAlbums(catalog.Albums, albumLimit) {
		deps.fillGenius(ctx, &catalog, source.Lookup{
			Name: artist.Name, GeniusID: artist.GeniusID, TrackLimit: limit - len(catalog.Tracks),
			Base: catalog, NeedArtist: needArtist, StudioGroups: groups, AlbumLimit: albumLimit,
		})
	}

	catalog.Name = firstKnown(catalog.Name, musicBrainzName)
	catalog.Tracks = uniqueTrackTitles(catalog.Tracks)
	catalog.Tracks = catalog.Tracks[:min(limit, len(catalog.Tracks))]
	reuseSaved(&catalog, saved)

	catalog.Albums = pickStudioAlbums(catalog.Albums, groups, albumLimit)
	addMusicBrainzAlbums(&catalog, groups, saved, albumLimit)
	deps.fillAlbumPlays(ctx, &catalog)

	artist.YTMusicID = firstKnown(catalog.YTMusicID, artist.YTMusicID)

	a.fillTrackGaps(ctx, artist, deps, &catalog)
	reuseSaved(&catalog, saved)

	catalog.Tracks = catalog.Tracks[:min(limit, len(catalog.Tracks))]

	return catalog
}

func (deps parseDependencies) fillAlbumPlays(ctx context.Context, catalog *source.Catalog) {
	if deps.AlbumPlays == nil {
		return
	}

	lookup := source.Catalog{TopSongs: catalog.TopSongs, TopSongsPlaylist: catalog.TopSongsPlaylist}
	pending := make(map[string]*source.Album, len(catalog.Albums))

	for i := range catalog.Albums {
		album := &catalog.Albums[i]
		if album.YTMusicID != nil && album.YtPlays == nil && !album.SkipPlays {
			lookup.Albums = append(lookup.Albums, *album)
			pending[*album.YTMusicID] = album
		}
	}

	if len(lookup.Albums) == 0 {
		return
	}

	timeout := time.Duration(deps.Settings.TaskTimeoutSeconds) * time.Second / 8

	sourceCtx, stop := context.WithTimeout(ctx, timeout)
	defer stop()

	deps.progress("YouTube Music", fmt.Sprintf("Ищем прослушивания альбомов (%d)", len(lookup.Albums)))

	started := time.Now()
	data, err := deps.AlbumPlays(sourceCtx, deps.Settings, lookup)
	deps.sourceDone("YouTube Music", started, err)

	for _, album := range data.Albums {
		if album.YTMusicID != nil {
			if target := pending[*album.YTMusicID]; target != nil {
				target.YtPlays = album.YtPlays
			}
		}
	}

	catalog.Warnings = append(catalog.Warnings, data.Warnings...)
	warnSource(catalog, "YouTube Music", err)
}

func (deps parseDependencies) fillGenius(ctx context.Context, catalog *source.Catalog, lookup source.Lookup) {
	if deps.Source == nil {
		return
	}

	sourceCtx, stop := context.WithTimeout(ctx, time.Duration(deps.Settings.TaskTimeoutSeconds)*time.Second/5)
	defer stop()

	deps.progress("Genius", "Заполняем пропуски")

	started := time.Now()
	data, err := deps.Source.Catalog(sourceCtx, lookup)

	mergeCatalog(catalog, data)
	warnSource(catalog, "Genius", err)
	deps.sourceDone("Genius", started, err)
}

func readYouTubeMusic(
	ctx context.Context, artist domain.Artist, deps parseDependencies, saved []source.Song,
) source.Catalog {
	var catalog source.Catalog

	if deps.YouTubeMusic != nil {
		sourceCtx, stop := context.WithTimeout(ctx, time.Duration(deps.Settings.TaskTimeoutSeconds)*time.Second/5)

		deps.progress("YouTube Music", "Ищем популярные альбомы и треки")

		started := time.Now()
		data, err := deps.YouTubeMusic(sourceCtx, deps.Settings, artist, saved)
		deps.sourceDone("YouTube Music", started, err)

		stop()

		catalog = data
		if artist.FullName != "" || slices.Contains(artist.ManualFields, "full_name") {
			catalog.Name = artist.FullName
		}

		if artist.LogoDownloadID != nil && !slices.Contains(artist.MissingFields, "logo") {
			catalog.LogoURL = ""
		}

		warnSource(&catalog, "YouTube Music", err)
	}

	return catalog
}

func importTrackLimit(artist domain.Artist, subsFromSource *int64) int {
	subs := firstKnown(artist.YtSubs, subsFromSource)
	if slices.Contains(artist.ManualFields, "yt_subs") {
		subs = artist.YtSubs
	}

	return ParseTrackLimit(subs)
}

func (a *App) fillTrackGaps(
	ctx context.Context, artist *domain.Artist, deps parseDependencies, catalog *source.Catalog,
) {
	a.resolveFallbackTracks(ctx, artist, deps, catalog)

	if deps.Enrichment == nil || artist.MusicBrainzID == "" {
		return
	}

	timeout := time.Duration(deps.Settings.TaskTimeoutSeconds) * time.Second / 7
	sourceCtx, stop := context.WithTimeout(ctx, timeout)

	deps.progress("MusicBrainz", "Дополняем обложки и длительности")

	started := time.Now()

	warnSource(catalog, "MusicBrainz", deps.Enrichment.AlbumCovers(sourceCtx, catalog.Albums))

	existing := len(catalog.Tracks)
	limit := importTrackLimit(*artist, catalog.YtSubs)
	tracks, err := deps.Enrichment.Tracks(sourceCtx, artist.MusicBrainzID, catalog.Tracks, limit)

	stop()

	catalog.Tracks = tracks
	warnSource(catalog, "MusicBrainz", err)
	deps.sourceDone("MusicBrainz", started, err)

	if len(tracks) > existing {
		newTracks := source.Catalog{Tracks: tracks[existing:]}
		a.resolveFallbackTracks(ctx, artist, deps, &newTracks)
		catalog.Warnings = append(catalog.Warnings, newTracks.Warnings...)
	}
}

func readMusicBrainz(
	ctx context.Context, mb *source.MusicBrainz, artist *domain.Artist, catalog *source.Catalog,
) ([]source.Album, string) {
	albumLimit := domain.AlbumImportLimit(firstKnown(artist.YtSubs, catalog.YtSubs))
	albums := pickStudioAlbums(catalog.Albums, nil, albumLimit)
	needGroups := source.NeedsAlbums(albums, albumLimit) || slices.ContainsFunc(albums, func(album source.Album) bool {
		return album.MusicBrainzID == ""
	})
	limit := importTrackLimit(*artist, catalog.YtSubs)
	needsTracks := len(catalog.Tracks) < limit || slices.ContainsFunc(catalog.Tracks, func(track source.Song) bool {
		return track.Duration == nil
	})
	missingArtist := artist.Country == "" && !slices.Contains(artist.ManualFields, "country") ||
		!artist.MembersKnown && !slices.Contains(artist.ManualFields, "artists") ||
		artist.FullName == "" && catalog.Name == "" && !slices.Contains(artist.ManualFields, "full_name")

	var name string

	if missingArtist || artist.MusicBrainzID == "" && (needGroups || needsTracks) {
		var data source.Enrichment

		var err error
		if artist.MusicBrainzID != "" {
			data, err = mb.EnrichByID(ctx, artist.MusicBrainzID)
		} else {
			data, err = mb.Enrich(ctx, artist.Name)
		}

		warnSource(catalog, "MusicBrainz", err)

		if err == nil {
			artist.MusicBrainzID = data.ID
			artist.Country = firstKnown(artist.Country, data.Country)

			if !artist.MembersKnown && data.MembersKnown {
				artist.Artists, artist.MembersKnown = data.Members, true
			}

			name = data.FullName
		}
	}

	if !needGroups || artist.MusicBrainzID == "" {
		return nil, name
	}

	groups, err := mb.Albums(ctx, artist.MusicBrainzID)
	warnSource(catalog, "MusicBrainz", err)

	return groups, name
}

func pickStudioAlbums(candidates, groups []source.Album, limit int) []source.Album {
	albums := make([]source.Album, 0, limit)
	for _, album := range candidates {
		if !source.IsStudioAlbum(album) || slices.ContainsFunc(albums, func(old source.Album) bool {
			return source.SameAlbum(old, album) || sameTitle(old.Name, album.Name)
		}) {
			continue
		}

		group, found := matchingGroup(album, groups)
		if found && !source.IsStudioAlbum(group) {
			continue
		}

		if found {
			album.MusicBrainzID = group.MusicBrainzID
		}

		albums = append(albums, album)
		if len(albums) == limit {
			break
		}
	}

	return albums
}

func rankedAlbumIDs(ranked, groups []source.Album, limit int) []string {
	var ids []string

	for _, album := range pickStudioAlbums(ranked, groups, limit) {
		if album.YTMusicID != nil {
			ids = append(ids, *album.YTMusicID)
		}
	}

	return ids
}

func matchingGroup(album source.Album, groups []source.Album) (source.Album, bool) {
	index, ambiguous := albumMatch(groups, album)
	if ambiguous || index < 0 {
		return source.Album{}, false
	}

	return groups[index], true
}

func addMusicBrainzAlbums(catalog *source.Catalog, groups []source.Album, saved source.Catalog, limit int) {
	for i := range catalog.Albums {
		album := &catalog.Albums[i]
		if group, found := matchingGroup(*album, groups); found {
			album.ReleaseDate = firstKnown(album.ReleaseDate, group.ReleaseDate)
		}
	}

	catalog.Albums = pickStudioAlbums(append(catalog.Albums, groups...), groups, limit)
	reuseSaved(catalog, saved)
}

func (a *App) resolveFallbackTracks(
	ctx context.Context, artist *domain.Artist, deps parseDependencies, catalog *source.Catalog,
) {
	if deps.ResolveTracks == nil {
		return
	}

	var tracks []source.Song

	for _, track := range catalog.Tracks {
		if track.YTMusicID == nil {
			tracks = append(tracks, track)
		}
	}

	if len(tracks) == 0 {
		return
	}

	sourceCtx, stop := context.WithTimeout(ctx, time.Duration(deps.Settings.TaskTimeoutSeconds)*time.Second/12)

	deps.progress("YouTube Music", fmt.Sprintf("Сопоставляем треки: %d", len(tracks)))

	started := time.Now()
	data, err := deps.ResolveTracks(sourceCtx, deps.Settings, *artist, tracks)
	deps.sourceDone("YouTube Music", started, err)

	stop()
	mergeCatalog(catalog, data)
	warnSource(catalog, "YouTube Music", err)
}

func warnSource(c *source.Catalog, name string, err error) {
	if err != nil {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s: %v", name, err))
	}
}

func (a *App) savedCatalog(ctx context.Context, id uuid.UUID) (source.Catalog, error) {
	var saved source.Catalog

	page := domain.Page{ArtistID: &id, Limit: 200}
	albums := map[uuid.UUID]source.Album{}

	for {
		rows, err := a.uow.Albums.List(ctx, page)
		if err != nil {
			return saved, err
		}

		for _, row := range rows {
			album := source.Album{
				ID: derefID(row.GeniusID), Name: row.Name, YTMusicID: row.YTMusicID, MusicBrainzID: row.MusicBrainzID,
				ReleaseDate: row.ReleaseDate, HasCover: row.CoverDownloadID != nil && !slices.Contains(row.MissingFields, "cover"),
				YtPlays: row.SourceYtPlays, SkipPlays: slices.Contains(row.ManualFields, "yt_plays"),
			}
			saved.Albums = append(saved.Albums, album)
			albums[row.ID] = album
		}

		if len(rows) < page.Limit {
			break
		}

		page.Offset += len(rows)
	}

	page.Offset = 0

	for {
		rows, err := a.uow.Tracks.List(ctx, page)
		if err != nil {
			return saved, err
		}

		for _, row := range rows {
			song := source.Song{
				ID: derefID(row.GeniusID), Title: row.Name, YTMusicID: row.YTMusicID,
				MusicBrainzID: row.MusicBrainzID, Duration: row.DurationSeconds,
			}
			if row.YTMusicID != nil {
				song.YtPlays = row.YtPlays
			}

			if row.AlbumID != nil {
				if album, ok := albums[*row.AlbumID]; ok {
					song.Album = &album
				}
			}

			saved.Tracks = append(saved.Tracks, song)
		}

		if len(rows) < page.Limit {
			break
		}

		page.Offset += len(rows)
	}

	return saved, nil
}

func reuseSaved(c *source.Catalog, saved source.Catalog) {
	for i := range c.Albums {
		album := &c.Albums[i]
		if index, _ := albumMatch(saved.Albums, *album); index >= 0 {
			old := saved.Albums[index]
			album.ID = firstKnown(album.ID, old.ID)
			album.MusicBrainzID = firstKnown(album.MusicBrainzID, old.MusicBrainzID)
			album.YTMusicID = firstKnown(album.YTMusicID, old.YTMusicID)
			album.ReleaseDate = firstKnown(old.ReleaseDate, album.ReleaseDate)
			album.YtPlays = firstKnown(old.YtPlays, album.YtPlays)
			album.SkipPlays = old.SkipPlays
			album.HasCover = old.HasCover

			if old.HasCover {
				album.CoverURL = ""
			}
		}
	}

	for i := range c.Tracks {
		song := &c.Tracks[i]
		if index, _ := trackMatch(saved.Tracks, *song); index >= 0 {
			old := saved.Tracks[index]
			song.ID = firstKnown(song.ID, old.ID)
			song.MusicBrainzID = firstKnown(song.MusicBrainzID, old.MusicBrainzID)
			song.YTMusicID = firstKnown(song.YTMusicID, old.YTMusicID)
			song.YtPlays = firstKnown(old.YtPlays, song.YtPlays)
			song.Duration = firstKnown(old.Duration, song.Duration)
			song.Album = firstKnown(song.Album, old.Album)
		}
	}
}

func derefID(id *int64) int64 {
	if id == nil {
		return 0
	}

	return *id
}
