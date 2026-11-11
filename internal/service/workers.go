package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
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

	if d.SourceType == "unknown" && d.Downloader == "" {
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
		var resolved domain.Download

		resolved, err = a.resolveAudio(taskCtx, s, d)
		if err == nil {
			var artist, title string
			artist, title, err = a.uow.Downloads.DownloadNames(taskCtx, d.ID)
			if err == nil {
				resolved.Meta["artist"], resolved.Meta["title"] = artist, title
			}
		}
		if err == nil {
			err = downloadFile(taskCtx, s, &resolved, directory)
			d.URI, d.Meta, d.Preview, d.FileType = resolved.URI, resolved.Meta, resolved.Preview, resolved.FileType
		}
	}

	if ctx.Err() != nil {
		// При остановке процесса захват истечёт, и задача продолжится после перезапуска.
		return ctx.Err()
	}

	d.Status, d.Error = "finished", ""
	if err != nil {
		d.URI, d.Error, d.Status = "", err.Error(), "pending"

		var httpErr *source.HTTPError

		permanent := errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrNotFound) ||
			(errors.As(err, &httpErr) && (httpErr.Status == 403 || httpErr.Status == 404))
		if d.Attempts >= 3 || permanent {
			d.Status = "errored"
		}

		a.log.Warn("ошибка скачивания", "download_id", d.ID, "attempt", d.Attempts, "error", err)
	}

	seconds, ok := d.Meta["duration_seconds"].(float64)
	if d.Status != "finished" || d.FileType != "mp3" || d.Preview || !ok || seconds > math.MaxInt32 {
		return a.uow.Downloads.Finish(ctx, d)
	}

	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if err := tx.Downloads.Finish(ctx, d); err != nil {
			return err
		}

		return tx.Downloads.FillTrackDuration(ctx, d.ID, max(1, int(math.Round(seconds))))
	})
}

func (a *App) parseNext(ctx context.Context, s domain.Settings) error {
	run, err := a.uow.Runs.Claim(ctx, s.TaskTimeoutSeconds+30)
	if err != nil {
		return err
	}

	if run.Provider != "" {
		return a.runProvider(ctx, run, s)
	}

	deps := parseDependencies{
		Settings: s, YouTubeMusic: source.YTMusicCatalog, ResolveTracks: source.YTMusicTracks,
		AlbumPlays: source.YTMusicAlbumPlays,
		Enrichment: source.NewMusicBrainz(s),
	}

	if a.token != "" {
		genius := source.NewGenius(s, a.token)
		genius.SetQuotaObserver(a.uow.Quota.Save)

		deps.Source = genius
	}

	a.parseEvent(ctx, run.ID, store.ParseEvent{Stage: "Запуск", Message: "Парсер начал работу"})

	err = a.processRun(ctx, &run, deps)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	run.Status = "finished"
	if err != nil {
		run.Status, run.Error = "errored", err.Error()
	}

	a.parseEvent(ctx, run.ID, store.ParseEvent{
		Stage: "Завершение", Message: fmt.Sprintf("Обработано %d, ошибок %d", run.Processed, run.Failed),
	})
	return errors.Join(err, a.uow.Runs.Finish(ctx, run))
}

type parseDependencies struct {
	Progress      func(string, string)
	YouTubeMusic  func(context.Context, domain.Settings, domain.Artist, []source.Song) (source.Catalog, error)
	ResolveTracks func(context.Context, domain.Settings, domain.Artist, []source.Song) (source.Catalog, error)
	AlbumPlays    func(context.Context, domain.Settings, source.Catalog) (source.Catalog, error)
	Settings      domain.Settings
	Source        source.CatalogSource
	Enrichment    *source.MusicBrainz
}

func (a *App) parseEvent(ctx context.Context, run uuid.UUID, event store.ParseEvent) {
	if err := a.uow.Runs.AddEvent(ctx, run, event); err != nil && ctx.Err() == nil {
		a.log.Warn("запись журнала парсинга", "run_id", run, "error", err)
	}
}

func (d parseDependencies) progress(stage, message string) {
	if d.Progress != nil {
		d.Progress(stage, message)
	}
}

func (d parseDependencies) sourceDone(stage string, started time.Time, err error) {
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		d.progress(stage, fmt.Sprintf("Ошибка за %s: %v", elapsed, err))
	} else {
		d.progress(stage, fmt.Sprintf("Шаг завершён за %s", elapsed))
	}
}

func (a *App) processRun(ctx context.Context, run *domain.ParseRun, deps parseDependencies) error {
	for {
		current, err := a.uow.Runs.Renew(ctx, *run, deps.Settings.TaskTimeoutSeconds+30)
		if err != nil {
			return err
		}

		if current.Status == "stopping" {
			return nil
		}

		artist, err := a.uow.Artists.Claim(ctx, deps.Settings.TaskTimeoutSeconds+30, run.CreatedAt)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}

		if err != nil {
			return err
		}

		deps.Progress = func(stage, message string) {
			a.parseEvent(ctx, run.ID, store.ParseEvent{Artist: artist.Name, Stage: stage, Message: message})
		}
		deps.progress("Исполнитель", "Начали обработку")

		err = a.parseArtistWithRetry(ctx, run, artist, deps)
		deps.sourceDone("Исполнитель", artist.UpdatedAt, err)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if errors.Is(err, errParsingStopped) {
			return a.uow.Artists.Release(ctx, artist.ID)
		}

		if errors.Is(err, context.DeadlineExceeded) {
			return errors.Join(err, a.uow.Artists.Release(ctx, artist.ID))
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

var errParsingStopped = errors.New("парсинг остановлен")

func (a *App) parseArtistWithRetry(
	ctx context.Context, run *domain.ParseRun, artist domain.Artist, deps parseDependencies,
) error {
	const delay = 3 * time.Minute
	for attempt := 0; ; attempt++ {
		err := a.parseArtist(ctx, artist, deps)
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || attempt == 3 {
			return err
		}

		deps.progress("Повтор", "Тайм-аут. Следующая попытка через 3 минуты")
		a.log.Warn("тайм-аут парсинга, ожидаем повтор", "artist_id", artist.ID, "retry", attempt+1, "wait", delay, "error", err)

		if err := a.waitParseRetry(ctx, run, artist.ID, deps.Settings); err != nil {
			return err
		}
	}
}

func (a *App) waitParseRetry(ctx context.Context, run *domain.ParseRun, artistID uuid.UUID, s domain.Settings) error {
	deadline := time.Now().Add(3 * time.Minute)

	for {
		current, err := a.uow.Runs.Renew(ctx, *run, s.TaskTimeoutSeconds+30)
		if err != nil {
			return err
		}

		if current.Status == "stopping" {
			return errParsingStopped
		}

		if err := a.uow.Artists.Renew(ctx, artistID, s.TaskTimeoutSeconds+30); err != nil {
			return err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}

		if !wait(ctx, min(remaining, 2*time.Second)) {
			return ctx.Err()
		}
	}
}

func (a *App) parseArtist(ctx context.Context, artist domain.Artist, deps parseDependencies) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(deps.Settings.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	saved, err := a.savedCatalog(ctx, artist.ID)
	if err != nil {
		return err
	}

	catalog := a.collectCatalog(ctx, &artist, deps, saved)

	subs := firstKnown(artist.YtSubs, catalog.YtSubs)
	if slices.Contains(artist.ManualFields, "yt_subs") {
		subs = artist.YtSubs
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	artist.GeniusID = firstKnown(optionalID(catalog.ArtistID), artist.GeniusID)
	artist.YTMusicID = firstKnown(catalog.YTMusicID, artist.YTMusicID)
	artist.FullName = firstKnown(catalog.Name, artist.FullName)
	artist.YtSubs = subs

	if !slices.Contains(artist.ManualFields, "yt_plays") {
		artist.YtPlays = firstKnown(artist.YtPlays, firstKnown(catalog.YtPlays, estimatePlays(subs)))
	}

	deps.progress("Каталог", fmt.Sprintf("Сохраняем альбомы (%d) и треки (%d)", len(catalog.Albums), len(catalog.Tracks)))

	for _, warning := range catalog.Warnings {
		deps.progress("Предупреждение", warning)
	}
	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		return persistCatalog(ctx, tx, artist, catalog)
	})
}

// ParseTrackLimit возвращает лимит треков по числу подписчиков YouTube Music.
func ParseTrackLimit(subs *int64) int {
	if subs == nil {
		return 1
	}

	for i, threshold := range []int64{100_000, 300_000, 500_000, 1_000_000, 2_000_000, 5_000_000, 10_000_000} {
		if *subs < threshold {
			return i + 1
		}
	}

	return 8
}

func estimatePlays(subs *int64) *int64 {
	if subs == nil {
		return nil
	}

	plays := min(*subs, math.MaxInt64/2) * 2

	return &plays
}

func optionalID(id int64) *int64 {
	if id == 0 {
		return nil
	}

	return &id
}

func persistCatalog(ctx context.Context, tx *store.UnitOfWork, artist domain.Artist, c source.Catalog) error {
	logo, err := imageTask(ctx, tx, c.LogoURL)
	if err != nil {
		return err
	}

	artist.LogoDownloadID = logo
	albums := make([]domain.Album, 0, len(c.Albums))

	for _, album := range c.Albums {
		cover, err := imageTask(ctx, tx, album.CoverURL)
		if err != nil {
			return err
		}

		saved, err := tx.Albums.Import(ctx, domain.Album{
			ID: uuid.New(), Name: album.Name, ArtistID: artist.ID, GeniusID: optionalID(album.ID), YTMusicID: album.YTMusicID,
			MusicBrainzID: album.MusicBrainzID, SourceYtPlays: album.YtPlays, ReleaseDate: album.ReleaseDate,
			TrackList: album.TrackList, TrackListComplete: album.Complete, CoverDownloadID: cover,
		})
		if err != nil {
			return err
		}

		albums = append(albums, saved)
	}

	for _, song := range c.Tracks {
		var album *domain.Album

		if song.Album != nil {
			i, ambiguous := albumMatch(c.Albums, *song.Album)
			if i >= 0 {
				album = &albums[i]
			}

			if ambiguous {
				c.Warnings = append(c.Warnings, fmt.Sprintf("трек %q: альбом не определён однозначно", song.Title))
			}
		}

		track := domain.Track{
			ID: uuid.New(), Name: song.Title, ArtistID: artist.ID, GeniusID: optionalID(song.ID),
			MusicBrainzID: song.MusicBrainzID, YTMusicID: song.YTMusicID, YtPlays: song.YtPlays, DurationSeconds: song.Duration,
		}
		if album != nil {
			track.AlbumID = &album.ID
		}

		track, err := tx.Tracks.Import(ctx, track, artist.YtPlays)
		if err != nil {
			return err
		}

		if err := prepareAudio(ctx, tx, track, artist.Name); err != nil {
			return err
		}
	}

	var shortlist []uuid.UUID

	for _, album := range albums {
		if album.YTMusicID != nil && slices.Contains(c.RankedAlbumIDs, *album.YTMusicID) {
			shortlist = append(shortlist, album.ID)
		}
	}

	if len(shortlist) != domain.AlbumImportLimit(artist.YtSubs) {
		shortlist = nil
	}
	if err := tx.Albums.SaveShortlist(ctx, artist.ID, shortlist); err != nil {
		return err
	}

	artist.Error = strings.Join(c.Warnings, "; ")

	return tx.Artists.Imported(ctx, artist)
}

func imageTask(ctx context.Context, tx *store.UnitOfWork, address string) (*uuid.UUID, error) {
	if address == "" {
		return nil, nil
	}

	if err := domain.ValidateURL(address); err != nil {
		return nil, err
	}

	d, err := tx.Downloads.Create(ctx, domain.Download{
		ID: uuid.New(), SourceType: "image", SourceURI: address, FileType: "image", Meta: map[string]any{},
	})

	return &d.ID, err
}

func prepareAudio(ctx context.Context, tx *store.UnitOfWork, track domain.Track, artistName string) error {
	exists, err := tx.Downloads.HasAudioJob(ctx, track.ID)
	if err != nil || exists {
		return err
	}

	task, err := tx.Downloads.Create(ctx, domain.Download{
		ID: uuid.New(), SourceType: "unknown", SourceURI: "track:" + track.ID.String(), FileType: "mp3",
		Meta: map[string]any{"title": track.Name, "artist": artistName},
	})
	if err != nil {
		return err
	}

	return tx.Downloads.Link(ctx, track.ID, task.ID)
}
