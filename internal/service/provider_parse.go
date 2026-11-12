package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
	"devkeeper.com/quiz/internal/store"
)

type providerParser struct {
	app              *App
	run              domain.ParseRun
	settings         domain.Settings
	artist           domain.Artist
	genius           *source.Genius
	mb               *source.MusicBrainz
	albums           []domain.Album
	tracks           []domain.TrackWithStatus
	rankedTracks     bool
	rankedCandidates []source.Song
	lists            []string
	issues           []string
	failureKind      string
	failureCount     int
	affected         map[uuid.UUID]bool
	nextYTRequest    time.Time
}

var errProviderStopped = errors.New("провайдер остановлен из-за серии сбоев")

func (a *App) runProvider(ctx context.Context, run domain.ParseRun, settings domain.Settings) error {
	p := providerParser{app: a, run: run, settings: settings}
	switch run.Provider {
	case "genius":
		p.genius = source.NewGenius(settings, a.token)
		p.genius.UseParserRetries()
		p.genius.SetQuotaObserver(a.uow.Quota.Save)
	case "musicbrainz":
		p.mb = source.NewMusicBrainz(settings)
		p.mb.UseParserRetries()
	}
	p.event(ctx, "Запуск", run.Provider)
	err := p.process(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.run.Status = "finished"
	if err != nil {
		p.run.Status, p.run.Error = "errored", err.Error()
		if errors.Is(err, errProviderStopped) {
			p.run.Status = "stopped"
		}
	}
	p.event(ctx, "Завершение", fmt.Sprintf("Полей %d, альбомов %d, треков %d, ошибок %d",
		p.run.Fields, p.run.Albums, p.run.Tracks, p.run.Errors))
	if errors.Is(err, errProviderStopped) {
		err = nil
	}
	return errors.Join(err, a.uow.Runs.Finish(ctx, p.run))
}

func (p *providerParser) process(ctx context.Context) error {
	for {
		current, err := p.app.uow.Runs.Renew(ctx, p.run, p.settings.TaskTimeoutSeconds+30)
		if err != nil {
			return err
		}
		if current.Status == "stopping" {
			return nil
		}
		p.artist, err = p.app.uow.Artists.ClaimProvider(ctx, p.settings.TaskTimeoutSeconds+30, p.run.CreatedAt,
			p.run.Provider)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		p.issues = nil
		artistCtx, cancel := context.WithTimeout(ctx, time.Duration(p.settings.TaskTimeoutSeconds)*time.Second)
		p.lists, err = p.app.uow.Artists.ProviderLists(artistCtx, p.artist.ID, p.run.Provider, domain.AlbumImportLimit(p.artist.YtSubs))
		if err == nil {
			err = p.loadRows(artistCtx)
		}
		if err == nil {
			p.event(artistCtx, "Исполнитель", "Начали обработку")
			switch p.run.Provider {
			case "yt_music":
				err = p.youtube(artistCtx)
			case "genius":
				err = p.parseGenius(artistCtx)
			case "musicbrainz":
				err = p.parseMusicBrainz(artistCtx)
			}
		}
		timedOut := errors.Is(artistCtx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errParsingStopped) {
			return p.app.uow.Artists.Release(ctx, p.artist.ID)
		}
		if timedOut {
			err = fmt.Errorf("тайм-аут обработки исполнителя (%d с)", p.settings.TaskTimeoutSeconds)
			p.event(ctx, "Исполнитель", err.Error())
		}
		if err != nil && !errors.Is(err, errProviderStopped) {
			p.issues = append(p.issues, err.Error())
		}
		p.run.Processed++
		if len(p.issues) > 0 {
			p.run.Failed++
		}
		finishErr := p.app.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
			if err := tx.Artists.FinishProvider(ctx, p.artist.ID, strings.Join(p.issues, " | ")); err != nil {
				return err
			}
			return tx.Runs.SaveProviderCounts(ctx, p.run)
		})
		if finishErr != nil {
			return finishErr
		}
		if err != nil && !timedOut {
			return err
		}
	}
}

func (p *providerParser) event(ctx context.Context, stage, message string) {
	p.app.parseEvent(ctx, p.run.ID, store.ParseEvent{Artist: p.artist.Name, Stage: stage, Message: message})
}

func (p *providerParser) request(ctx context.Context, label string, id uuid.UUID, call func(context.Context) error) (bool, error) {
	scenario, _, _ := strings.Cut(label, " · ")
	lease := max(p.settings.TaskTimeoutSeconds, p.settings.HTTPTimeoutSeconds+p.settings.Source429MaxDelaySeconds) + 30
	p.event(ctx, label, "Запрос")
	started := time.Now()
	var err error
	for attempt := 0; ; attempt++ {
		current, renewErr := p.app.uow.Runs.Renew(ctx, p.run, lease)
		if renewErr != nil {
			return false, renewErr
		}
		if current.Status == "stopping" {
			return false, errParsingStopped
		}
		if err := p.app.uow.Artists.Renew(ctx, p.artist.ID, lease); err != nil {
			return false, err
		}
		if p.run.Provider == "yt_music" {
			if delay := time.Until(p.nextYTRequest); delay > 0 && !wait(ctx, delay) {
				return false, ctx.Err()
			}
			p.nextYTRequest = time.Now().Add(time.Second)
		}
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(p.settings.HTTPTimeoutSeconds)*time.Second)
		err = call(requestCtx)
		cancel()
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err == nil {
			p.failureKind, p.failureCount, p.affected = "", 0, nil
			if id != p.artist.ID {
				if err := p.app.uow.Runs.SetProviderError(ctx, id, p.run.Provider, scenario, false); err != nil {
					return false, err
				}
			}
			p.event(ctx, label, fmt.Sprintf("Ответ получен за %s", time.Since(started).Round(time.Millisecond)))
			return true, nil
		}
		if attempt == p.settings.Source429Retries {
			break
		}
		delay := time.Duration(min((attempt+1)*p.settings.Source429DelaySeconds, p.settings.Source429MaxDelaySeconds)) * time.Second
		var response *source.HTTPError
		if errors.As(err, &response) {
			if response.RetryAfter > time.Duration(p.settings.Source429MaxDelaySeconds)*time.Second {
				break
			}
			delay = max(delay, response.RetryAfter)
		}
		p.event(ctx, label, fmt.Sprintf("Повтор %d через %s: %v", attempt+1, delay, err))
		if !wait(ctx, delay) {
			return false, ctx.Err()
		}
	}
	kind := providerFailureKind(err)
	if p.failureKind != kind {
		p.failureKind, p.failureCount, p.affected = kind, 0, map[uuid.UUID]bool{}
	}
	p.failureCount++
	p.affected[id] = true
	p.run.Errors++
	message := fmt.Sprintf("%s: %v", label, err)
	p.issues = append(p.issues, message)
	p.event(ctx, label, fmt.Sprintf("Ошибка: %v. Серия %s: %d, сущностей %d", err, kind, p.failureCount, len(p.affected)))
	if saveErr := p.app.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if id != p.artist.ID {
			if err := tx.Runs.SetProviderError(ctx, id, p.run.Provider, scenario, true); err != nil {
				return err
			}
		}
		return tx.Runs.SaveProviderCounts(ctx, p.run)
	}); saveErr != nil {
		return false, saveErr
	}
	if p.failureCount >= p.settings.ParserStopFailures && len(p.affected) > 1 {
		return false, fmt.Errorf("%w: %s, подряд %d, сущностей %d, последний запрос: %s",
			errProviderStopped, kind, p.failureCount, len(p.affected), message)
	}
	return false, nil
}

func providerFailureKind(err error) string {
	var httpErr *source.HTTPError
	var ytErr *source.RequestFailure
	var dns *net.DNSError
	var network net.Error
	var command *exec.ExitError
	switch {
	case errors.As(err, &httpErr):
		return fmt.Sprintf("HTTP %d (%s)", httpErr.Status, httpErr.Base)
	case errors.As(err, &ytErr):
		if ytErr.Status != 0 {
			return fmt.Sprintf("HTTP %d", ytErr.Status)
		}
		return ytErr.Kind
	case errors.Is(err, context.DeadlineExceeded):
		return "тайм-аут"
	case errors.As(err, &dns):
		return "DNS"
	case errors.As(err, &network):
		if network.Timeout() {
			return "тайм-аут"
		}
		var op *net.OpError
		if errors.As(err, &op) {
			return fmt.Sprintf("сеть: %s: %T", op.Op, op.Err)
		}
		return fmt.Sprintf("сеть: %T", network)
	case errors.As(err, &command):
		return fmt.Sprintf("процесс: код %d", command.ExitCode())
	default:
		return fmt.Sprintf("%T", err)
	}
}

func (p *providerParser) loadRows(ctx context.Context) error {
	p.albums, p.tracks = nil, nil
	page := domain.Page{ArtistID: &p.artist.ID, Limit: 200}
	for {
		rows, err := p.app.uow.Albums.List(ctx, page)
		if err != nil {
			return err
		}
		p.albums = append(p.albums, rows...)
		if len(rows) < page.Limit {
			break
		}
		page.Offset += len(rows)
	}
	page.Offset = 0
	for {
		rows, err := p.app.uow.Tracks.List(ctx, page)
		if err != nil {
			return err
		}
		p.tracks = append(p.tracks, rows...)
		if len(rows) < page.Limit {
			return nil
		}
		page.Offset += len(rows)
	}
}

func (p *providerParser) needsList(kind string, count, limit int) bool {
	return count < limit && !slices.Contains(p.lists, kind)
}
