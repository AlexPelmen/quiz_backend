package service

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
	"devkeeper.com/quiz/internal/store"
)

func (p *providerParser) save(ctx context.Context, c source.Catalog, mb source.Enrichment, lists ...string) error {
	previousRun := p.run
	err := p.app.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if err := p.saveArtist(ctx, tx, c, mb); err != nil {
			return err
		}
		for _, album := range c.Albums {
			if err := p.saveAlbum(ctx, tx, album); err != nil {
				return err
			}
		}
		for _, track := range c.Tracks {
			if err := p.saveTrack(ctx, tx, track); err != nil {
				return err
			}
		}
		for _, kind := range lists {
			if err := tx.Artists.FinishProviderList(ctx, p.artist.ID, p.run.Provider, kind, domain.AlbumImportLimit(p.artist.YtSubs)); err != nil {
				return err
			}
		}
		return tx.Runs.SaveProviderCounts(ctx, p.run)
	})
	if err == nil {
		p.lists = append(p.lists, lists...)
	} else {
		p.run = previousRun
	}
	return err
}

func (p *providerParser) saveArtist(ctx context.Context, tx *store.UnitOfWork, c source.Catalog, mb source.Enrichment) error {
	old, err := tx.Artists.Get(ctx, p.artist.ID)
	if err != nil {
		return err
	}
	data := domain.Artist{
		ID: old.ID, FullName: firstKnown(c.Name, mb.FullName), Country: mb.Country, Artists: mb.Members,
		MembersKnown: mb.MembersKnown, YtSubs: c.YtSubs, YtPlays: c.YtPlays, YTMusicID: c.YTMusicID,
		GeniusID: optionalID(c.ArtistID), MusicBrainzID: mb.ID,
	}
	if data.Artists == nil {
		data.Artists = []string{}
	}
	needLogo := slices.Contains(old.MissingFields, "logo")
	if needLogo && c.LogoURL != "" {
		if domain.ValidateURL(c.LogoURL) != nil {
			p.event(ctx, "Логотип", "Источник вернул некорректную ссылку")
		} else {
			data.LogoDownloadID, err = imageTask(ctx, tx, c.LogoURL)
			if err != nil {
				return err
			}
		}
	}
	if err := tx.Artists.FillProvider(ctx, data); err != nil {
		return err
	}
	updated, err := tx.Artists.Get(ctx, old.ID)
	if err != nil {
		return err
	}
	p.artist = updated
	p.run.Fields += newFields(
		old.FullName == "" && updated.FullName != "", old.Country == "" && updated.Country != "",
		!old.MembersKnown && updated.MembersKnown, old.YtSubs == nil && updated.YtSubs != nil,
		old.YtPlays == nil && updated.YtPlays != nil, old.YTMusicID == nil && updated.YTMusicID != nil,
		old.GeniusID == nil && updated.GeniusID != nil, old.MusicBrainzID == "" && updated.MusicBrainzID != "",
		needLogo && !slices.Contains(updated.MissingFields, "logo"),
	)
	return nil
}

func asSourceAlbum(a domain.Album) source.Album {
	return source.Album{ID: derefID(a.GeniusID), Name: a.Name, YTMusicID: a.YTMusicID, MusicBrainzID: a.MusicBrainzID,
		ReleaseDate: a.ReleaseDate, HasCover: !slices.Contains(a.MissingFields, "cover"), YtPlays: a.YtPlays}
}

func (p *providerParser) albumIndex(album source.Album) (int, bool) {
	known := make([]source.Album, 0, len(p.albums))
	for _, a := range p.albums {
		known = append(known, asSourceAlbum(a))
	}
	return albumMatch(known, album)
}

func (p *providerParser) saveAlbum(ctx context.Context, tx *store.UnitOfWork, album source.Album) error {
	index, ambiguous := p.albumIndex(album)
	if ambiguous {
		p.event(ctx, "Альбом", "Неоднозначное совпадение: "+album.Name)
		return nil
	}
	var old domain.Album
	if index >= 0 {
		old = p.albums[index]
	} else if len(p.albums) >= domain.AlbumImportLimit(p.artist.YtSubs) || !source.IsStudioAlbum(album) {
		return nil
	}
	album.Name = firstKnown(old.Name, album.Name)
	data := domain.Album{ID: uuid.New(), ArtistID: p.artist.ID, Name: album.Name, GeniusID: optionalID(album.ID),
		YTMusicID: album.YTMusicID, MusicBrainzID: album.MusicBrainzID, ReleaseDate: album.ReleaseDate, SourceYtPlays: album.YtPlays}
	needCover := index < 0 || slices.Contains(old.MissingFields, "cover")
	if needCover && album.CoverURL != "" {
		if domain.ValidateURL(album.CoverURL) != nil {
			p.event(ctx, "Обложка · "+album.Name, "Источник вернул некорректную ссылку")
		} else {
			var err error
			data.CoverDownloadID, err = imageTask(ctx, tx, album.CoverURL)
			if err != nil {
				return err
			}
		}
	}
	saved, err := tx.Albums.Fill(ctx, data)
	if err != nil {
		return err
	}
	if index >= 0 {
		p.albums[index] = saved
	} else {
		p.albums = append(p.albums, saved)
		p.run.Albums++
	}
	p.run.Fields += newFields(index < 0, old.GeniusID == nil && saved.GeniusID != nil, old.YTMusicID == nil && saved.YTMusicID != nil,
		old.MusicBrainzID == "" && saved.MusicBrainzID != "", old.ReleaseDate == "" && saved.ReleaseDate != "",
		old.YtPlays == nil && saved.YtPlays != nil, needCover && !slices.Contains(saved.MissingFields, "cover"))
	return nil
}

func (p *providerParser) asSourceTrack(t domain.Track) source.Song {
	song := source.Song{ID: derefID(t.GeniusID), Title: t.Name, YTMusicID: t.YTMusicID, MusicBrainzID: t.MusicBrainzID,
		Duration: t.DurationSeconds, YtPlays: t.YtPlays}
	if t.AlbumID != nil {
		for _, album := range p.albums {
			if album.ID == *t.AlbumID {
				value := asSourceAlbum(album)
				song.Album = &value
				break
			}
		}
	}
	return song
}

func (p *providerParser) saveTrack(ctx context.Context, tx *store.UnitOfWork, song source.Song) error {
	known := make([]source.Song, 0, len(p.tracks))
	for _, track := range p.tracks {
		known = append(known, p.asSourceTrack(track.Track))
	}
	index, ambiguous := trackMatch(known, song)
	if ambiguous {
		p.event(ctx, "Трек", "Неоднозначное совпадение: "+song.Title)
		return nil
	}
	var old domain.Track
	if index >= 0 {
		old = p.tracks[index].Track
	} else if song.Title == "" || len(p.tracks) >= ParseTrackLimit(p.artist.YtSubs) {
		return nil
	}
	data := domain.Track{ID: uuid.New(), ArtistID: p.artist.ID, Name: firstKnown(old.Name, song.Title),
		GeniusID: optionalID(song.ID), YTMusicID: song.YTMusicID, MusicBrainzID: song.MusicBrainzID,
		YtPlays: song.YtPlays, DurationSeconds: song.Duration}
	if song.Album != nil {
		if i, _ := p.albumIndex(*song.Album); i >= 0 {
			data.AlbumID = &p.albums[i].ID
		}
	}
	saved, previous, err := tx.Tracks.Fill(ctx, data)
	if err != nil {
		return err
	}
	if err := prepareAudio(ctx, tx, saved, p.artist.Name); err != nil {
		return err
	}
	if savedIndex := slices.IndexFunc(p.tracks, func(track domain.TrackWithStatus) bool {
		return track.ID == saved.ID
	}); savedIndex >= 0 {
		p.tracks[savedIndex].Track = saved
	} else {
		p.tracks = append(p.tracks, domain.TrackWithStatus{Track: saved})
	}
	created := previous.ID == uuid.Nil
	if created {
		p.run.Tracks++
	}
	p.run.Fields += newFields(created, previous.AlbumID == nil && saved.AlbumID != nil,
		previous.GeniusID == nil && saved.GeniusID != nil, previous.YTMusicID == nil && saved.YTMusicID != nil,
		previous.MusicBrainzID == "" && saved.MusicBrainzID != "", previous.YtPlays == nil && saved.YtPlays != nil,
		previous.DurationSeconds == nil && saved.DurationSeconds != nil)
	return nil
}

func newFields(values ...bool) int {
	count := 0
	for _, filled := range values {
		if filled {
			count++
		}
	}
	return count
}
