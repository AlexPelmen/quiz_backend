package service

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

func (p *providerParser) yt(ctx context.Context, label, op, key string, id uuid.UUID, tracks ...source.Song) (source.Catalog, bool, error) {
	var data source.Catalog
	ok, err := p.request(ctx, label, id, func(ctx context.Context) error {
		var err error
		data, err = source.YTMusicRequest(ctx, p.settings, op, key, p.artist, tracks)
		return err
	})
	return data, ok, err
}

func (p *providerParser) needsYTAlbums() bool {
	return !slices.Contains(p.lists, "albums") && (len(p.albums) < domain.AlbumImportLimit(p.artist.YtSubs) ||
		slices.ContainsFunc(p.albums, func(a domain.Album) bool {
			return a.YTMusicID == nil && (a.ReleaseDate == "" || a.YtPlays == nil || slices.Contains(a.MissingFields, "cover"))
		}))
}

func (p *providerParser) youtube(ctx context.Context) error {
	needTracks := p.needsList("tracks", len(p.tracks), ParseTrackLimit(p.artist.YtSubs))
	var selected *source.Catalog
	if p.artist.YTMusicID == nil {
		label, operation, key := "Поиск ID исполнителя", "search_artist", ""
		if p.artist.YTMusicURL != "" {
			label, operation, key = "Ссылка на YouTube Music", "resolve_artist_url", p.artist.YTMusicURL
		}
		data, ok, err := p.yt(ctx, label, operation, key, p.artist.ID)
		if err != nil {
			return err
		}
		if ok {
			if len(data.ArtistCandidates) > 1 {
				selected, err = p.selectYTArtist(ctx, data.ArtistCandidates)
				if err != nil {
					return err
				}
				if selected != nil {
					data.YTMusicID = selected.YTMusicID
				}
			} else if data.YTMusicID == nil {
				p.event(ctx, "Поиск ID исполнителя", "Нет совпадений по названию")
			}
			if data.YTMusicID != nil {
				if err = p.save(ctx, source.Catalog{YTMusicID: data.YTMusicID}, source.Enrichment{}); err != nil {
					return err
				}
			}
		}
	}
	if p.artist.YTMusicID != nil && (selected != nil || p.needsYTAlbums() || needTracks || p.artist.YtSubs == nil ||
		p.artist.YtPlays == nil || p.artist.FullName == "" || slices.Contains(p.artist.MissingFields, "logo")) {
		if err := p.youtubeArtist(ctx, selected); err != nil {
			return err
		}
	}
	for _, track := range slices.Clone(p.tracks) {
		if track.YTMusicID == nil {
			data, ok, err := p.yt(ctx, "Поиск трека · "+track.Name, "search_song", "", track.ID, p.asSourceTrack(track.Track))
			if err != nil {
				return err
			}
			if !ok || len(data.Tracks) == 0 {
				continue
			}
			if err = p.save(ctx, data, source.Enrichment{}); err != nil {
				return err
			}
			track.YTMusicID = data.Tracks[0].YTMusicID
			track.DurationSeconds = firstKnown(track.DurationSeconds, data.Tracks[0].Duration)
			track.YtPlays = firstKnown(track.YtPlays, data.Tracks[0].YtPlays)
		}
		if track.YTMusicID != nil && (track.DurationSeconds == nil || track.YtPlays == nil) {
			data, ok, err := p.yt(ctx, "Карточка трека · "+track.Name, "song", *track.YTMusicID, track.ID)
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, data, source.Enrichment{}); err != nil {
					return err
				}
			}
		}
	}
	for _, album := range slices.Clone(p.albums) {
		if album.YTMusicID == nil || (album.ReleaseDate != "" && album.YtPlays != nil && !slices.Contains(album.MissingFields, "cover")) {
			continue
		}
		data, ok, err := p.yt(ctx, "Карточка альбома · "+album.Name, "album", *album.YTMusicID, album.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		for i := range data.Tracks {
			data.Tracks[i].Album = &data.Albums[0]
		}
		data.Tracks = slices.DeleteFunc(data.Tracks, func(song source.Song) bool {
			return !slices.ContainsFunc(p.tracks, func(track domain.TrackWithStatus) bool {
				return track.YTMusicID != nil && song.YTMusicID != nil && *track.YTMusicID == *song.YTMusicID
			})
		})
		if err = p.save(ctx, data, source.Enrichment{}); err != nil {
			return err
		}
	}
	return nil
}

func (p *providerParser) selectYTArtist(ctx context.Context, candidates []string) (*source.Catalog, error) {
	var selected *source.Catalog
	bestRank := [2]int64{-1, -1}
	for _, candidate := range candidates {
		card, ok, err := p.yt(ctx, "Карточка кандидата · "+candidate, "artist", candidate, p.artist.ID)
		if err != nil || !ok {
			return nil, err
		}
		if card.ArtistUnavailable {
			p.event(ctx, "Карточка кандидата · "+candidate, "Пропущена: YouTube Music не вернул музыкальную карточку")
			continue
		}
		rank := ytArtistPopularity(card)
		if slices.Compare(rank[:], bestRank[:]) > 0 {
			card.YTMusicID = &candidate
			selected, bestRank = &card, rank
		}
	}
	if selected == nil {
		p.event(ctx, "Поиск ID исполнителя", "У одноимённых исполнителей нет данных о популярности")
	} else {
		p.event(ctx, "Поиск ID исполнителя", "Выбрана самая популярная карточка · "+*selected.YTMusicID)
	}
	return selected, nil
}

func ytArtistPopularity(card source.Catalog) [2]int64 {
	rank := [2]int64{-1, -1}
	for i, metric := range [...]*int64{card.YtSubs, card.YtPlays} {
		if metric != nil {
			rank[i] = *metric
		}
	}
	return rank
}

func (p *providerParser) youtubeArtist(ctx context.Context, selected *source.Catalog) error {
	var data source.Catalog
	var err error
	if selected != nil {
		data = *selected
	} else {
		var ok bool
		data, ok, err = p.yt(ctx, "Карточка исполнителя", "artist", "", p.artist.ID)
		if err != nil || !ok {
			return err
		}
	}
	albums, tracks := data.Albums, data.Tracks
	data.Albums = nil
	oldLimit := domain.AlbumImportLimit(p.artist.YtSubs)
	if err = p.save(ctx, data, source.Enrichment{}); err != nil {
		return err
	}
	albumLimit := domain.AlbumImportLimit(p.artist.YtSubs)
	if albumLimit != oldLimit {
		p.lists, err = p.app.uow.Artists.ProviderLists(ctx, p.artist.ID, p.run.Provider, albumLimit)
		if err != nil {
			return err
		}
	}
	if p.needsYTAlbums() {
		albumsComplete := true
		rankedAlbums := false
		if data.AlbumBrowseID != "" && data.AlbumParams != "" {
			ranked, ok, queryErr := p.yt(ctx, "Популярные альбомы", "albums", data.AlbumBrowseID+"\n"+data.AlbumParams, p.artist.ID)
			if ok && len(ranked.Albums) > 0 {
				albums = ranked.Albums
				rankedAlbums = true
			}
			if queryErr != nil {
				return errors.Join(queryErr, p.save(ctx, source.Catalog{Albums: albums, Tracks: tracks}, source.Enrichment{}))
			}
			albumsComplete = ok
		}
		var lists []string
		if albumsComplete {
			lists = append(lists, "albums")
		}
		if err = p.save(ctx, source.Catalog{Albums: albums}, source.Enrichment{}, lists...); err != nil {
			return err
		}
		if rankedAlbums {
			var shortlist []uuid.UUID
			for _, album := range pickStudioAlbums(albums, nil, albumLimit) {
				if index, _ := p.albumIndex(album); index >= 0 {
					shortlist = append(shortlist, p.albums[index].ID)
				}
			}
			if len(shortlist) == albumLimit {
				if err = p.app.uow.Albums.SaveShortlist(ctx, p.artist.ID, shortlist); err != nil {
					return err
				}
			}
		}
	} else {
		// Уже заполненная подборка всё ещё может дать метаданные известных релизов.
		if err = p.save(ctx, source.Catalog{Albums: albums}, source.Enrichment{}); err != nil {
			return err
		}
	}
	if err = p.save(ctx, source.Catalog{Tracks: tracks}, source.Enrichment{}); err != nil {
		return err
	}
	if p.needsList("tracks", len(p.tracks), ParseTrackLimit(p.artist.YtSubs)) {
		if data.TopSongsPlaylist != "" {
			playlist, ok, err := p.yt(ctx, "Популярные треки", "playlist", data.TopSongsPlaylist, p.artist.ID)
			if err != nil || !ok {
				return err
			}
			return p.save(ctx, playlist, source.Enrichment{}, "tracks")
		}
		return p.save(ctx, source.Catalog{}, source.Enrichment{}, "tracks")
	}
	return nil
}

func (p *providerParser) parseGenius(ctx context.Context) error {
	needArtist := p.artist.FullName == "" || slices.Contains(p.artist.MissingFields, "logo")
	needAlbums := !slices.Contains(p.lists, "albums") && (len(p.albums) < domain.AlbumImportLimit(p.artist.YtSubs) ||
		slices.ContainsFunc(p.albums, func(a domain.Album) bool {
			return a.GeniusID == nil && (a.ReleaseDate == "" || slices.Contains(a.MissingFields, "cover"))
		}))
	needTracks := p.needsList("tracks", len(p.tracks), ParseTrackLimit(p.artist.YtSubs))
	if p.artist.GeniusID == nil && (needArtist || needAlbums || needTracks) {
		if err := p.findGeniusArtist(ctx); err != nil {
			return err
		}
	}
	if p.artist.GeniusID != nil {
		id := *p.artist.GeniusID
		if needArtist {
			var data source.Catalog
			ok, err := p.request(ctx, "Карточка исполнителя", p.artist.ID, func(ctx context.Context) error {
				var err error
				data, err = p.genius.Artist(ctx, id)
				return err
			})
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, data, source.Enrichment{}); err != nil {
					return err
				}
			}
		}
		if needAlbums {
			var albums []source.Album
			ok, err := p.request(ctx, "Подборка альбомов", p.artist.ID, func(ctx context.Context) error {
				var err error
				albums, err = p.genius.Albums(ctx, id)
				return err
			})
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, source.Catalog{Albums: albums}, source.Enrichment{}, "albums"); err != nil {
					return err
				}
			}
		}
		if needTracks {
			if err := p.geniusTracks(ctx, id); err != nil {
				return err
			}
		}
	}
	for _, album := range slices.Clone(p.albums) {
		if album.GeniusID == nil || (album.ReleaseDate != "" && !slices.Contains(album.MissingFields, "cover")) {
			continue
		}
		var detail source.Album
		ok, err := p.request(ctx, "Карточка альбома · "+album.Name, album.ID, func(ctx context.Context) error {
			var err error
			detail, err = p.genius.Album(ctx, asSourceAlbum(album))
			return err
		})
		if err != nil {
			return err
		}
		if ok {
			if err = p.save(ctx, source.Catalog{Albums: []source.Album{detail}}, source.Enrichment{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *providerParser) findGeniusArtist(ctx context.Context) error {
	for _, search := range source.GeniusArtistSearches(p.artist.Name) {
		for page := 1; page <= 5; page++ {
			var more bool
			ok, err := p.request(ctx, fmt.Sprintf("Поиск ID исполнителя, страница %d", page), p.artist.ID, func(ctx context.Context) error {
				var err error
				more, err = p.genius.SearchArtistPage(ctx, search, page)
				return err
			})
			if err != nil || !ok {
				return err
			}
			if !more {
				break
			}
		}
		if id, matched := search.Result(); matched {
			if id != 0 {
				return p.save(ctx, source.Catalog{ArtistID: id}, source.Enrichment{})
			}
			break
		}
	}
	p.event(ctx, "Поиск ID исполнителя", "Нет однозначного совпадения")
	return nil
}

func (p *providerParser) geniusTracks(ctx context.Context, id int64) error {
	for page := 1; page <= 2; page++ {
		var tracks []source.Song
		var more bool
		ok, err := p.request(ctx, "Популярные треки", p.artist.ID, func(ctx context.Context) error {
			var err error
			tracks, more, err = p.genius.Songs(ctx, id, page)
			return err
		})
		if err != nil || !ok {
			return err
		}
		if err = p.save(ctx, source.Catalog{Tracks: tracks}, source.Enrichment{}); err != nil {
			return err
		}
		if !more || len(p.tracks) >= ParseTrackLimit(p.artist.YtSubs) {
			break
		}
	}
	return p.save(ctx, source.Catalog{}, source.Enrichment{}, "tracks")
}

func (p *providerParser) parseMusicBrainz(ctx context.Context) error {
	needArtist := p.artist.Country == "" || !p.artist.MembersKnown || p.artist.FullName == ""
	needAlbums := !slices.Contains(p.lists, "albums") && (len(p.albums) < domain.AlbumImportLimit(p.artist.YtSubs) ||
		slices.ContainsFunc(p.albums, func(a domain.Album) bool {
			return a.MusicBrainzID == "" && (a.ReleaseDate == "" || slices.Contains(a.MissingFields, "cover"))
		}))
	needTracks := p.needsList("tracks", len(p.tracks), ParseTrackLimit(p.artist.YtSubs))
	if p.artist.MusicBrainzID == "" && (needArtist || needAlbums || needTracks ||
		slices.ContainsFunc(p.tracks, func(t domain.TrackWithStatus) bool { return t.DurationSeconds == nil && t.MusicBrainzID == "" })) {
		var id string
		ok, err := p.request(ctx, "Поиск ID исполнителя", p.artist.ID, func(ctx context.Context) error {
			var err error
			id, err = p.mb.FindArtist(ctx, p.artist.Name)
			return err
		})
		if err != nil {
			return err
		}
		if ok && id != "" {
			if err = p.save(ctx, source.Catalog{}, source.Enrichment{ID: id}); err != nil {
				return err
			}
		} else if ok {
			p.event(ctx, "Поиск ID исполнителя", "Нет однозначного совпадения")
		}
	}
	id := p.artist.MusicBrainzID
	if id != "" {
		if needArtist {
			var data source.Enrichment
			ok, err := p.request(ctx, "Страна и состав исполнителя", p.artist.ID, func(ctx context.Context) error {
				var err error
				data, err = p.mb.EnrichByID(ctx, id)
				return err
			})
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, source.Catalog{}, data); err != nil {
					return err
				}
			}
		}
		if needAlbums {
			var albums []source.Album
			ok, err := p.request(ctx, "Группы релизов", p.artist.ID, func(ctx context.Context) error {
				var err error
				albums, err = p.mb.Albums(ctx, id)
				return err
			})
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, source.Catalog{Albums: albums}, source.Enrichment{}, "albums"); err != nil {
					return err
				}
			}
		}
		if needTracks {
			var tracks []source.Song
			ok, err := p.request(ctx, "Записи исполнителя", p.artist.ID, func(ctx context.Context) error {
				var err error
				tracks, err = p.mb.ListRecordings(ctx, id)
				return err
			})
			if err != nil {
				return err
			}
			if ok {
				if err = p.save(ctx, source.Catalog{Tracks: tracks}, source.Enrichment{}, "tracks"); err != nil {
					return err
				}
			}
		}
	}
	for _, track := range slices.Clone(p.tracks) {
		if track.DurationSeconds != nil || (id == "" && track.MusicBrainzID == "") {
			continue
		}
		var data source.Song
		ok, err := p.request(ctx, "Длительность · "+track.Name, track.ID, func(ctx context.Context) error {
			var err error
			data, err = p.mb.Recording(ctx, id, p.asSourceTrack(track.Track))
			return err
		})
		if err != nil {
			return err
		}
		if ok {
			if err = p.save(ctx, source.Catalog{Tracks: []source.Song{data}}, source.Enrichment{}); err != nil {
				return err
			}
		}
	}
	for _, album := range slices.Clone(p.albums) {
		if album.MusicBrainzID == "" || !slices.Contains(album.MissingFields, "cover") {
			continue
		}
		data := asSourceAlbum(album)
		ok, err := p.request(ctx, "Cover Art Archive · "+album.Name, album.ID, func(ctx context.Context) error {
			var err error
			data.CoverURL, err = p.mb.AlbumCover(ctx, album.MusicBrainzID)
			return err
		})
		if err != nil {
			return err
		}
		if ok {
			if err = p.save(ctx, source.Catalog{Albums: []source.Album{data}}, source.Enrichment{}); err != nil {
				return err
			}
		}
	}
	return nil
}
