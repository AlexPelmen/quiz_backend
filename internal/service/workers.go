package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
	"devkeeper.com/quiz/internal/store"
)

// RunWorkers работает до отмены контекста и дожидается завершения всех горутин.
func (a *App) RunWorkers(ctx context.Context) {
	var wg sync.WaitGroup

	wg.Go(func() { a.loop(ctx, a.parseNext) })

	for index := range 16 {
		wg.Go(func() {
			a.loop(ctx, func(ctx context.Context, s domain.Settings) error {
				if index >= s.DownloadWorkers {
					return domain.ErrNotFound
				}

				return a.downloadNext(ctx, s)
			})
		})
	}

	wg.Wait()
}

func (a *App) loop(ctx context.Context, work func(context.Context, domain.Settings) error) {
	for ctx.Err() == nil {
		s, err := a.Settings(ctx)
		if err == nil {
			err = work(ctx, s)
		}

		if ctx.Err() != nil {
			return
		}

		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			a.log.Error("фоновая обработка", "error", err)
		}

		seconds := max(1, s.PollSeconds)
		if err == nil {
			seconds = 0
		}

		if !wait(ctx, time.Duration(seconds)*time.Second) {
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (a *App) downloadNext(ctx context.Context, s domain.Settings) error {
	d, err := a.uow.Downloads.Claim(ctx, s.TaskTimeoutSeconds+30)
	if err != nil {
		return err
	}

	if d.Meta == nil {
		d.Meta = map[string]any{}
	}

	if d.SourceType == "unknown" {
		d.Status = "manual"
		return a.uow.Downloads.Finish(ctx, d)
	}

	taskCtx, cancel := context.WithTimeout(ctx, time.Duration(s.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	directory := "music/tracks"
	if d.SourceType == "image" {
		isArtist, lookupErr := a.uow.Downloads.ImageIsArtist(taskCtx, d.ID)
		if lookupErr != nil {
			err = lookupErr
		} else if isArtist {
			directory = "images/artists"
		} else {
			directory = "images/albums"
		}
	}
	if err == nil {
		err = downloadFile(taskCtx, s, &d, directory)
	}

	if ctx.Err() != nil {
		// При остановке процесса захват истечёт, и задача продолжится после перезапуска.
		return ctx.Err()
	}

	d.Status = "finished"
	if err != nil {
		d.URI, d.Error, d.Status = "", err.Error(), "pending"
		if d.Attempts >= 3 {
			d.Status = "errored"
		}

		a.log.Warn("ошибка скачивания", "download_id", d.ID, "attempt", d.Attempts, "error", err)
	}

	return a.uow.Downloads.Finish(ctx, d)
}

func (a *App) parseNext(ctx context.Context, s domain.Settings) error {
	run, err := a.uow.Runs.Claim(ctx, s.TaskTimeoutSeconds+30)
	if err != nil {
		return err
	}

	genius := source.NewGenius(s, a.token)
	genius.SetQuotaObserver(a.uow.Quota.Save)

	mb := source.NewMusicBrainz(s)
	err = a.processRun(ctx, &run, parseDependencies{Settings: s, Source: genius, Enrichment: mb})

	if ctx.Err() != nil {
		return ctx.Err()
	}

	run.Status = "finished"
	if err != nil {
		run.Status, run.Error = "errored", err.Error()
	}

	return errors.Join(err, a.uow.Runs.Finish(ctx, run))
}

type parseDependencies struct {
	Settings   domain.Settings
	Source     source.CatalogSource
	Enrichment *source.MusicBrainz
}

func (a *App) processRun(ctx context.Context, run *domain.ParseRun, deps parseDependencies) error {
	for {
		if err := a.uow.Runs.Renew(ctx, *run, deps.Settings.TaskTimeoutSeconds+30); err != nil {
			return err
		}

		artist, err := a.uow.Artists.Claim(ctx, deps.Settings.TaskTimeoutSeconds+30, run.CreatedAt)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}

		if err != nil {
			return err
		}

		err = a.parseArtist(ctx, artist, deps)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		var apiErr *source.HTTPError
		if errors.As(err, &apiErr) && apiErr.Status == 429 {
			// Клиент уже исчерпал повторы или время ожидания; оставляем исполнителя для следующего запуска.
			if failErr := a.uow.Artists.Fail(ctx, artist.ID, err.Error()); failErr != nil {
				return errors.Join(err, failErr)
			}

			if retryErr := a.uow.Artists.Retry(ctx, artist.ID); retryErr != nil {
				return errors.Join(err, retryErr)
			}

			return err
		}

		run.Processed++
		if err != nil {
			run.Failed++

			a.log.Warn("ошибка парсинга исполнителя", "artist_id", artist.ID, "error", err)

			if failErr := a.uow.Artists.Fail(ctx, artist.ID, err.Error()); failErr != nil {
				return errors.Join(err, failErr)
			}
		}
	}
}

func (a *App) parseArtist(ctx context.Context, artist domain.Artist, deps parseDependencies) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(deps.Settings.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	n := ParseTrackLimit(deps.Settings.KParse, artist.Points)

	c, err := deps.Source.Catalog(ctx, source.Lookup{Name: artist.Name, GeniusID: artist.GeniusID, TrackLimit: n})
	if err != nil {
		return err
	}

	artist.GeniusID, artist.FullName, artist.Artists = &c.ArtistID, c.Name, []string{}
	enrichment, enrichErr := deps.Enrichment.Enrich(ctx, artist.Name)

	if enrichErr == nil {
		artist.Country, artist.MusicBrainzID = enrichment.Country, enrichment.ID
		artist.FullName, artist.Artists = enrichment.FullName, enrichment.Members
	} else {
		c.Warnings = append(c.Warnings, enrichErr.Error())
	}

	artist.Error = strings.Join(c.Warnings, "; ")

	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		return persistCatalog(ctx, tx, catalogInput{Artist: artist, Catalog: c})
	})
}

// ParseTrackLimit вычисляет число импортируемых треков на исполнителя.
func ParseTrackLimit(k float64, points int) int {
	if k <= 0 || points <= 0 {
		return 0
	}

	return max(1, int(math.Floor(k*float64(points))))
}

type catalogInput struct {
	Artist  domain.Artist
	Catalog source.Catalog
}

func persistCatalog(ctx context.Context, tx *store.UnitOfWork, in catalogInput) error {
	artist, c := in.Artist, in.Catalog

	logo, err := imageTask(ctx, tx, c.LogoURL)
	if err != nil {
		return err
	}

	artist.LogoDownloadID = logo
	albums := map[int64]domain.Album{}

	for _, album := range c.Albums {
		cover, err := imageTask(ctx, tx, album.CoverURL)
		if err != nil {
			return err
		}

		saved, err := tx.Albums.Import(ctx, domain.Album{
			ID: uuid.New(), Name: album.Name, ArtistID: artist.ID, GeniusID: &album.ID,
			ReleaseDate: album.ReleaseDate, TrackList: album.TrackList,
			TrackListComplete: album.Complete, CoverDownloadID: cover,
		})
		if err != nil {
			return err
		}

		albums[album.ID] = saved
	}

	for _, song := range c.Tracks {
		if err := persistSong(ctx, tx, songInput{Artist: artist, Song: song, Albums: albums}); err != nil {
			return err
		}
	}

	return tx.Artists.Imported(ctx, artist)
}

func imageTask(ctx context.Context, tx *store.UnitOfWork, address string) (*uuid.UUID, error) {
	if address == "" {
		return nil, nil
	}

	d, err := tx.Downloads.Create(ctx, domain.Download{
		ID: uuid.New(), SourceType: "image",
		SourceURI: address, FileType: "image", Meta: map[string]any{},
	})

	return &d.ID, err
}

type songInput struct {
	Artist domain.Artist
	Song   source.Song
	Albums map[int64]domain.Album
}

func persistSong(ctx context.Context, tx *store.UnitOfWork, in songInput) error {
	t := domain.Track{ID: uuid.New(), Name: in.Song.Title, ArtistID: in.Artist.ID, GeniusID: &in.Song.ID}
	albumName := ""

	if in.Song.Album != nil {
		if album, ok := in.Albums[in.Song.Album.ID]; ok {
			t.AlbumID, albumName = &album.ID, album.Name
		}
	}

	t, err := tx.Tracks.Import(ctx, t)
	if err != nil {
		return err
	}

	tasks := songTasks(in.Song)
	if len(tasks) == 0 {
		tasks = []domain.Download{{SourceType: "unknown", SourceURI: fmt.Sprintf("unknown:genius:%d", in.Song.ID)}}
	}

	for _, task := range tasks {
		task.ID, task.FileType = uuid.New(), "mp3"
		task.Meta = map[string]any{"title": t.Name, "artist": in.Artist.Name, "album": albumName}

		d, err := tx.Downloads.Create(ctx, task)
		if err != nil {
			return err
		}

		if err := tx.Downloads.Link(ctx, t.ID, d.ID); err != nil {
			return err
		}
	}

	return nil
}

func songTasks(song source.Song) []domain.Download {
	r := []domain.Download{}
	seen := map[string]bool{}
	add := func(kind, address string) {
		if address != "" && !seen[address] && domain.ValidateURL(address) == nil {
			r = append(r, domain.Download{SourceType: kind, SourceURI: address})
			seen[address] = true
		}
	}
	add("youtube", song.YouTubeURL)

	for _, media := range song.Media {
		if media.Provider == "youtube" {
			add("youtube", media.URL)
			continue
		}

		u, err := url.Parse(media.URL)
		if err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".mp3") {
			add("mp3", media.URL)
		}
	}

	return r
}
