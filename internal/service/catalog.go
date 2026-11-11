// Package service реализует бизнес-операции каталога, загрузок и квизов.
package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// App объединяет сервисы одного процесса и их зависимости.
type App struct {
	uow   *store.UnitOfWork
	log   *slog.Logger
	token string
	media *Media
}

// New собирает приложение; секрет Genius не попадает в настройки API.
func New(uow *store.UnitOfWork, logger *slog.Logger, token string) *App {
	return &App{uow: uow, log: logger, token: token, media: &Media{}}
}

// Ready проверяет готовность подключения к хранилищу.
func (a *App) Ready(ctx context.Context) error {
	return a.uow.Ping(ctx)
}

// ReadRequest задаёт единый формат чтения списка или конкретного объекта.
type ReadRequest struct {
	Resource string
	ID       *uuid.UUID
	Page     domain.Page
}

// Read предоставляет каталожные данные HTTP-слою без прямого доступа к репозиториям.
func (a *App) Read(ctx context.Context, req ReadRequest) (any, error) {
	if req.ID != nil {
		return a.readOne(ctx, req.Resource, *req.ID)
	}

	switch req.Resource {
	case "artists":
		return a.uow.Artists.List(ctx, req.Page)
	case "albums":
		return a.uow.Albums.List(ctx, req.Page)
	case "tracks":
		return a.uow.Tracks.List(ctx, req.Page)
	case "downloads":
		return a.uow.Downloads.List(ctx, req.Page)
	case "parsing-runs":
		return a.uow.Runs.List(ctx, req.Page)
	case "quiz-packs":
		return a.uow.Packs.List(ctx, req.Page)
	default:
		return nil, domain.ErrNotFound
	}
}

func (a *App) readOne(ctx context.Context, resource string, id uuid.UUID) (any, error) {
	switch resource {
	case "artists":
		return a.uow.Artists.Get(ctx, id)
	case "albums":
		return a.uow.Albums.Get(ctx, id)
	case "tracks":
		return a.uow.Tracks.GetWithStatus(ctx, id)
	case "downloads":
		return a.uow.Downloads.Get(ctx, id)
	case "parsing-runs":
		return a.uow.Runs.Get(ctx, id)
	case "quiz-packs":
		return a.uow.Packs.Get(ctx, id)
	default:
		return nil, domain.ErrNotFound
	}
}

// Delete удаляет объект каталога или пакет с проверкой ссылочной целостности в БД.
func (a *App) Delete(ctx context.Context, resource string, id uuid.UUID) error {
	switch resource {
	case "artists":
		return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
			return tx.Artists.Delete(ctx, id)
		})
	case "albums":
		return a.uow.Albums.Delete(ctx, id)
	case "tracks":
		return a.uow.Tracks.Delete(ctx, id)
	case "quiz-packs":
		return a.uow.Packs.Delete(ctx, id)
	default:
		return domain.ErrNotFound
	}
}

// ArtistForm поддерживает создание и частичное обновление исполнителя.
type ArtistForm struct {
	Status           *string   `json:"status"`
	Name             *string   `json:"name"`
	Tags             *[]string `json:"tags"`
	YtPlays          *int64    `json:"yt_plays"`
	YtSubs           *int64    `json:"yt_subs"`
	ExcludedFromQuiz *bool     `json:"excluded_from_quiz"`
	FullName         *string   `json:"full_name"`
	Country          *string   `json:"country"`
	Artists          *[]string `json:"artists"`
	YTMusicURL       *string   `json:"yt_music_url"`
}

// SaveArtist создаёт исполнителя либо применяет непустые поля формы.
func (a *App) SaveArtist(ctx context.Context, id *uuid.UUID, f ArtistForm) (domain.Artist, error) {
	var result domain.Artist

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if f.Status != nil && (id == nil || (*f.Status != "manual" && *f.Status != "created")) {
			return fmt.Errorf("%w: статус можно изменить на manual или created", domain.ErrInvalid)
		}

		r := domain.Artist{ID: uuid.New(), Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Artists.Get(ctx, *id)
			if err != nil {
				return err
			}

			if (f.Status != nil || f.YTMusicURL != nil) && r.Status == "processing" {
				return fmt.Errorf("%w: исполнитель сейчас обрабатывается", domain.ErrConflict)
			}
			if f.YTMusicURL != nil && r.YTMusicID != nil && strings.TrimSpace(*f.YTMusicURL) != r.YTMusicURL {
				return fmt.Errorf("%w: ID YouTube Music уже установлен", domain.ErrInvalid)
			}
		}

		applyArtist(&r, f)
		if f.YTMusicURL != nil && r.YTMusicURL != "" {
			u, err := url.Parse(r.YTMusicURL)
			if err != nil || u.Scheme != "https" || u.Host != "music.youtube.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^/(@[\pL\pN_.-]+|channel/UC[A-Za-z0-9_-]{22})/?$`).MatchString(u.Path) {
				return fmt.Errorf("%w: укажите ссылку на страницу исполнителя music.youtube.com", domain.ErrInvalid)
			}
		}

		if err := validateCatalogName(r.Name); err != nil {
			return err
		}

		if f.YtPlays != nil && *f.YtPlays < 0 || f.YtSubs != nil && *f.YtSubs < 0 {
			return fmt.Errorf("%w: метрики YouTube не могут быть отрицательными", domain.ErrInvalid)
		}

		var err error
		if id == nil {
			result, err = tx.Artists.Create(ctx, r)
		} else {
			result, err = tx.Artists.Update(ctx, r)
			if err == nil && f.Status != nil {
				if err = tx.Artists.SetStatus(ctx, r.ID, *f.Status); err == nil {
					result, err = tx.Artists.Get(ctx, r.ID)
				}
			} else if err == nil && f.YTMusicURL != nil && r.YTMusicID == nil && r.YTMusicURL != "" {
				if err = tx.Artists.SetStatus(ctx, r.ID, "created"); err == nil {
					result, err = tx.Artists.Get(ctx, r.ID)
				}
			}
		}

		return err
	})

	return result, err
}

func applyArtist(r *domain.Artist, f ArtistForm) {
	if f.YTMusicURL != nil {
		r.YTMusicURL = strings.TrimSpace(*f.YTMusicURL)
	}
	if f.Name != nil {
		r.Name = strings.TrimSpace(*f.Name)
	}

	if f.ExcludedFromQuiz != nil {
		r.ExcludedFromQuiz = *f.ExcludedFromQuiz
	}

	if f.YtPlays != nil {
		r.YtPlays = f.YtPlays
		markManual(&r.ManualFields, "yt_plays")
	}

	if f.YtSubs != nil {
		r.YtSubs = f.YtSubs
		markManual(&r.ManualFields, "yt_subs")

		if r.YtPlays == nil && *r.YtSubs >= 0 && *r.YtSubs <= math.MaxInt64/2 {
			plays := *r.YtSubs * 2
			r.YtPlays = &plays
		}
	}

	if f.Tags != nil {
		r.Tags = *f.Tags
	}

	if r.Artists == nil {
		r.Artists = []string{}
	}

	if r.ManualFields == nil {
		r.ManualFields = []string{}
	}

	for field, value := range map[string]*string{"full_name": f.FullName, "country": f.Country} {
		if value == nil {
			continue
		}

		if field == "country" {
			r.Country = strings.TrimSpace(*value)
		} else {
			r.FullName = strings.TrimSpace(*value)
		}

		if !slices.Contains(r.ManualFields, field) {
			r.ManualFields = append(r.ManualFields, field)
		}
	}

	if f.Artists != nil {
		r.Artists = *f.Artists
		r.MembersKnown = true

		if !slices.Contains(r.ManualFields, "artists") {
			r.ManualFields = append(r.ManualFields, "artists")
		}
	}
}

// CatalogForm задаёт поля создания или обновления альбома и трека.
type CatalogForm struct {
	Name             *string    `json:"name"`
	Tags             *[]string  `json:"tags"`
	YtPlays          *int64     `json:"yt_plays"`
	ExcludedFromQuiz *bool      `json:"excluded_from_quiz"`
	DurationSeconds  *int       `json:"duration_seconds"`
	TrackList        *string    `json:"track_list"`
	ArtistID         *uuid.UUID `json:"artist_id"`
	AlbumID          *uuid.UUID `json:"album_id"`
	ReleaseDate      *string    `json:"release_date"`
}

// SaveAlbum создаёт альбом либо изменяет его название, дату и оценку.
func (a *App) SaveAlbum(ctx context.Context, id *uuid.UUID, f CatalogForm) (domain.Album, error) {
	if f.AlbumID != nil || f.DurationSeconds != nil || (id != nil && f.ArtistID != nil) {
		return domain.Album{}, fmt.Errorf("%w: связь альбома нельзя изменить", domain.ErrInvalid)
	}

	var result domain.Album

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		r := domain.Album{ID: uuid.New(), Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Albums.Get(ctx, *id)
			if err != nil {
				return err
			}
		} else if f.ArtistID != nil {
			r.ArtistID = *f.ArtistID
		}

		if err := f.apply(catalogEdit{&r.Name, &r.Tags, &r.YtPlays, &r.ExcludedFromQuiz, &r.ManualFields}); err != nil {
			return err
		}

		if f.ReleaseDate != nil {
			r.ReleaseDate = strings.TrimSpace(*f.ReleaseDate)
			r.ManualReleaseDate = true
		}

		if f.TrackList != nil {
			r.TrackList = strings.TrimSpace(*f.TrackList)
			if r.TrackList != "" {
				rows, err := csv.NewReader(strings.NewReader(r.TrackList)).ReadAll()
				if err != nil || len(rows) < 2 || !slices.Equal(rows[0], []string{"position", "title"}) {
					return fmt.Errorf("%w: треклист должен быть CSV с заголовком position,title", domain.ErrInvalid)
				}
			}

			r.TrackListComplete = r.TrackList != ""
			markManual(&r.ManualFields, "track_list")
		}

		if r.ArtistID == uuid.Nil {
			return fmt.Errorf("%w: artist_id обязателен", domain.ErrInvalid)
		}

		var err error
		if id == nil {
			result, err = tx.Albums.Create(ctx, r)
		} else {
			result, err = tx.Albums.Update(ctx, r)
		}

		return err
	})

	return result, err
}

// SaveTrack создаёт трек либо изменяет его название и оценку.
func (a *App) SaveTrack(ctx context.Context, id *uuid.UUID, f CatalogForm) (domain.Track, error) {
	if f.ReleaseDate != nil || f.TrackList != nil || (id != nil && f.ArtistID != nil) {
		return domain.Track{}, fmt.Errorf("%w: недопустимое поле трека", domain.ErrInvalid)
	}

	var result domain.Track

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		r := domain.Track{ID: uuid.New(), Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Tracks.Get(ctx, *id)
			if err != nil {
				return err
			}
		} else if f.ArtistID != nil {
			r.ArtistID = *f.ArtistID
		}

		if f.AlbumID != nil {
			r.AlbumID = f.AlbumID
			if *f.AlbumID == uuid.Nil {
				r.AlbumID = nil
			}

			markManual(&r.ManualFields, "album_id")
		}

		if f.DurationSeconds != nil {
			if *f.DurationSeconds <= 0 || *f.DurationSeconds > math.MaxInt32 {
				return fmt.Errorf("%w: длительность должна быть положительным числом секунд", domain.ErrInvalid)
			}

			r.DurationSeconds = f.DurationSeconds
			markManual(&r.ManualFields, "duration_seconds")
		}

		if err := f.apply(catalogEdit{&r.Name, &r.Tags, &r.YtPlays, &r.ExcludedFromQuiz, &r.ManualFields}); err != nil {
			return err
		}

		if r.ArtistID == uuid.Nil {
			return fmt.Errorf("%w: artist_id обязателен", domain.ErrInvalid)
		}

		if f.AlbumID != nil && r.AlbumID != nil {
			album, err := tx.Albums.Get(ctx, *r.AlbumID)
			if err != nil {
				return err
			}

			if album.ArtistID != r.ArtistID {
				return fmt.Errorf("%w: альбом принадлежит другому исполнителю", domain.ErrInvalid)
			}
		}

		var err error
		if id == nil {
			result, err = tx.Tracks.Create(ctx, r)
			if err == nil {
				artist, lookupErr := tx.Artists.Get(ctx, r.ArtistID)
				if lookupErr != nil {
					return lookupErr
				}

				if err = prepareAudio(ctx, tx, result, artist.Name); err == nil {
					result, err = tx.Tracks.Get(ctx, result.ID)
				}
			}
		} else {
			result, err = tx.Tracks.Update(ctx, r)
		}

		return err
	})

	return result, err
}

type catalogEdit struct {
	name     *string
	tags     *[]string
	plays    **int64
	excluded *bool
	manual   *[]string
}

func (f CatalogForm) apply(edit catalogEdit) error {
	if f.Name != nil {
		*edit.name = strings.TrimSpace(*f.Name)
	}

	if f.Tags != nil {
		*edit.tags = *f.Tags
	}

	if f.ExcludedFromQuiz != nil {
		*edit.excluded = *f.ExcludedFromQuiz
	}

	if f.YtPlays != nil {
		if *f.YtPlays < 0 {
			return fmt.Errorf("%w: метрики YouTube не могут быть отрицательными", domain.ErrInvalid)
		}

		*edit.plays = f.YtPlays

		markManual(edit.manual, "yt_plays")
	}

	return validateCatalogName(*edit.name)
}

func markManual(fields *[]string, name string) {
	if !slices.Contains(*fields, name) {
		*fields = append(*fields, name)
	}
}

func validateCatalogName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 1000 {
		return fmt.Errorf("%w: требуется название до 1000 символов", domain.ErrInvalid)
	}

	return nil
}

// Settings возвращает текущую конфигурацию без секретов.
func (a *App) Settings(ctx context.Context) (domain.Settings, error) {
	return a.uow.Settings.Get(ctx)
}

// PatchSettings применяет JSON-форму поверх текущих настроек в транзакции.
func (a *App) PatchSettings(ctx context.Context, data []byte) (domain.Settings, error) {
	var result domain.Settings

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		s, err := tx.Settings.Get(ctx)
		if err != nil {
			return err
		}

		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()

		if err := d.Decode(&s); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
		}

		if err := s.Validate(); err != nil {
			return err
		}

		result = s

		return tx.Settings.Save(ctx, s)
	})

	return result, err
}

// StartParsing создаёт асинхронный запуск; новые исполнители не запускают его автоматически.
func (a *App) StartParsing(ctx context.Context, refresh bool) (domain.ParseRun, error) {
	if !refresh {
		return a.StartProviderParsing(ctx, "yt_music")
	}

	var run domain.ParseRun

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		var err error

		run, err = tx.Runs.CreateProvider(ctx, "yt_music")
		if err != nil {
			return err
		}

		return tx.Artists.QueueCatalogRefresh(ctx)
	})

	return run, err
}

// StartProviderParsing создаёт запуск одного поставщика данных.
func (a *App) StartProviderParsing(ctx context.Context, provider string) (domain.ParseRun, error) {
	switch provider {
	case "yt_music", "musicbrainz":
	case "genius":
		if a.token == "" {
			return domain.ParseRun{}, fmt.Errorf("%w: для Genius нужен GENIUS_ACCESS_TOKEN", domain.ErrInvalid)
		}
	default:
		return domain.ParseRun{}, fmt.Errorf("%w: неизвестный провайдер парсинга", domain.ErrInvalid)
	}
	return a.uow.Runs.CreateProvider(ctx, provider)
}

// ParseEvents возвращает журнал выбранного запуска.
func (a *App) ParseEvents(ctx context.Context, id uuid.UUID, page domain.Page) ([]store.ParseEvent, error) {
	return a.uow.Runs.Events(ctx, id, page)
}

// AlbumCleanupPreview перечисляет альбомы вне подтверждённой подборки.
func (a *App) AlbumCleanupPreview(ctx context.Context, page domain.Page) ([]store.AlbumCleanupRow, error) {
	return a.uow.Albums.CleanupPreview(ctx, page)
}

// CleanupAlbums атомарно удаляет выбранные альбомы, пригодные для очистки.
func (a *App) CleanupAlbums(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 || len(ids) > 200 {
		return fmt.Errorf("%w: выберите от 1 до 200 альбомов", domain.ErrInvalid)
	}

	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return fmt.Errorf("%w: неверный список альбомов", domain.ErrInvalid)
		}

		seen[id] = true
	}

	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		return tx.Albums.DeleteCleanupAlbums(ctx, ids)
	})
}

// StopParsing запрашивает остановку после текущего исполнителя.
func (a *App) StopParsing(ctx context.Context, id uuid.UUID) (domain.ParseRun, error) {
	return a.uow.Runs.Stop(ctx, id)
}

// RetryArtist разрешает повторную обработку при следующем запуске парсинга.
func (a *App) RetryArtist(ctx context.Context, id uuid.UUID) error {
	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if err := tx.Artists.Retry(ctx, id); err != nil {
			return err
		}
		return tx.Artists.ResetTrackRanking(ctx, id)
	})
}
