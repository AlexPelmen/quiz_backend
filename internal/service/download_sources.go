package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

func (a *App) resolveAudio(ctx context.Context, s domain.Settings, d domain.Download) (domain.Download, error) {
	switch d.Downloader {
	case "", "direct", "image":
		return d, nil
	}

	ids, err := a.uow.Downloads.TrackIDs(ctx, d.ID)
	if err != nil {
		return d, err
	}

	if len(ids) != 1 {
		return d, fmt.Errorf("%w: для общего задания нескольких треков укажите прямую ссылку", domain.ErrInvalid)
	}

	track, err := a.uow.Tracks.Get(ctx, ids[0])
	if err != nil {
		return d, err
	}

	artist, err := a.uow.Artists.Get(ctx, track.ArtistID)
	if err != nil {
		return d, err
	}

	var album string

	if track.AlbumID != nil {
		record, err := a.uow.Albums.Get(ctx, *track.AlbumID)
		if err != nil {
			return d, err
		}

		album = record.Name
	}

	d.Meta["title"], d.Meta["artist"], d.Meta["album"] = track.Name, artist.Name, album
	switch d.Downloader {
	case "yt_music":
		if track.YTMusicID == nil {
			if u, err := url.Parse(d.SourceURI); err == nil && u.Hostname() == "music.youtube.com" {
				d.SourceType = "youtube"
				return d, nil
			}

			return d, fmt.Errorf("%w: у трека нет ссылки YouTube Music", domain.ErrNotFound)
		}

		d.SourceURI = "https://music.youtube.com/watch?" + url.Values{"v": {*track.YTMusicID}}.Encode()
		d.SourceType = "youtube"
	case "hitmo":
		if track.DurationSeconds == nil {
			return d, fmt.Errorf("%w: Hitmo требует длительность трека", domain.ErrInvalid)
		}

		d.SourceURI, err = source.HitmoAudio(ctx, s, artist.Name+" - "+track.Name, *track.DurationSeconds)
		d.SourceType = "mp3"
		d.Meta["expected_duration_seconds"] = *track.DurationSeconds
	case "genius_youtube", "genius_short":
		return a.resolveGeniusAudio(ctx, s, d, track)
	default:
		return d, fmt.Errorf("%w: неизвестный загрузчик", domain.ErrInvalid)
	}

	return d, err
}

func (a *App) resolveGeniusAudio(
	ctx context.Context, s domain.Settings, d domain.Download, track domain.Track,
) (domain.Download, error) {
	if a.token == "" {
		if hasLegacyYouTubeLink(d) {
			return d, nil
		}

		return d, fmt.Errorf("%w: для Genius нужен GENIUS_ACCESS_TOKEN", domain.ErrInvalid)
	}

	genius := source.NewGenius(s, a.token)
	genius.SetQuotaObserver(a.uow.Quota.Save)

	song, err := genius.AudioSong(ctx, track, tagValue(d.Meta, "artist"), tagValue(d.Meta, "album"))
	if err != nil {
		if ctx.Err() == nil && hasLegacyYouTubeLink(d) {
			a.log.Warn("используется сохранённая ссылка YouTube", "download_id", d.ID, "error", err)
			return d, nil
		}

		return d, err
	}

	if d.Downloader == "genius_short" {
		d.SourceURI, err = genius.Preview(ctx, song)
		d.SourceType, d.Preview = "mp3", true

		return d, err
	}

	address := song.YouTubeURL
	if address == "" {
		for _, media := range song.Media {
			if strings.EqualFold(media.Provider, "youtube") && media.URL != "" {
				address = media.URL
				break
			}
		}
	}

	if address == "" && hasLegacyYouTubeLink(d) {
		address = d.SourceURI
	}

	if address == "" {
		return d, fmt.Errorf("%w: Genius не вернул ссылку YouTube", domain.ErrNotFound)
	}

	d.SourceURI, d.SourceType = address, "youtube"

	return d, nil
}

func hasLegacyYouTubeLink(d domain.Download) bool {
	return d.Downloader == "genius_youtube" && d.SourceType == "youtube" && d.SourceURI != ""
}
