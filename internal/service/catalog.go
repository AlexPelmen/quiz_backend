// Package service реализует бизнес-операции каталога, загрузок и квизов.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
		return a.uow.Artists.Delete(ctx, id)
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
	Status   *string   `json:"status"`
	Name     *string   `json:"name"`
	Tags     *[]string `json:"tags"`
	Points   *float64  `json:"points"`
	FullName *string   `json:"full_name"`
	Country  *string   `json:"country"`
	Artists  *[]string `json:"artists"`
}

// SaveArtist создаёт исполнителя либо применяет непустые поля формы.
func (a *App) SaveArtist(ctx context.Context, id *uuid.UUID, f ArtistForm) (domain.Artist, error) {
	var result domain.Artist

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		if f.Status != nil && (id == nil || (*f.Status != "manual" && *f.Status != "created")) {
			return fmt.Errorf("%w: статус можно изменить на manual или created", domain.ErrInvalid)
		}

		r := domain.Artist{ID: uuid.New(), Points: 100, Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Artists.Get(ctx, *id)
			if err != nil {
				return err
			}

			if f.Status != nil && r.Status == "processing" {
				return fmt.Errorf("%w: исполнитель сейчас обрабатывается", domain.ErrConflict)
			}
		}

		applyArtist(&r, f)

		if err := validateNamePoints(r.Name, r.Points); err != nil {
			return err
		}

		var err error
		if id == nil {
			result, err = tx.Artists.Create(ctx, r)
			if err == nil && (f.FullName != nil || f.Country != nil || f.Artists != nil) {
				r.ID = result.ID
				result, err = tx.Artists.Update(ctx, r)
			}
		} else {
			result, err = tx.Artists.Update(ctx, r)
			if err == nil && f.Status != nil {
				err = tx.Artists.SetStatus(ctx, r.ID, *f.Status)
				result.Status = *f.Status
			}
		}

		return err
	})

	return result, err
}

func applyArtist(r *domain.Artist, f ArtistForm) {
	if f.Name != nil {
		r.Name = strings.TrimSpace(*f.Name)
	}

	if f.Points != nil {
		r.Points = *f.Points
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
		if !slices.Contains(r.ManualFields, "artists") {
			r.ManualFields = append(r.ManualFields, "artists")
		}
	}
}

// CatalogForm задаёт поля создания или обновления альбома и трека.
type CatalogForm struct {
	Name        *string    `json:"name"`
	Tags        *[]string  `json:"tags"`
	Points      *float64   `json:"points"`
	ArtistID    *uuid.UUID `json:"artist_id"`
	AlbumID     *uuid.UUID `json:"album_id"`
	ReleaseDate *string    `json:"release_date"`
}

// SaveAlbum создаёт альбом либо изменяет его название, дату и оценку.
func (a *App) SaveAlbum(ctx context.Context, id *uuid.UUID, f CatalogForm) (domain.Album, error) {
	if f.AlbumID != nil || (id != nil && f.ArtistID != nil) {
		return domain.Album{}, fmt.Errorf("%w: связь альбома нельзя изменить", domain.ErrInvalid)
	}

	var result domain.Album

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		r := domain.Album{ID: uuid.New(), Points: 100, Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Albums.Get(ctx, *id)
			if err != nil {
				return err
			}
		} else if f.ArtistID != nil {
			r.ArtistID = *f.ArtistID
		}

		applyNamePoints(&r.Name, &r.Points, f)

		if f.Tags != nil {
			r.Tags = *f.Tags
		}

		if f.ReleaseDate != nil {
			r.ReleaseDate = *f.ReleaseDate
			r.ManualReleaseDate = true
		}

		if err := validateNamePoints(r.Name, r.Points); err != nil {
			return err
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
	if f.ReleaseDate != nil || (id != nil && (f.ArtistID != nil || f.AlbumID != nil)) {
		return domain.Track{}, fmt.Errorf("%w: недопустимое поле трека", domain.ErrInvalid)
	}

	var result domain.Track

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		r := domain.Track{ID: uuid.New(), Points: 100, Tags: []string{}}

		if id != nil {
			var err error

			r, err = tx.Tracks.Get(ctx, *id)
			if err != nil {
				return err
			}
		} else {
			if f.ArtistID != nil {
				r.ArtistID = *f.ArtistID
			}

			r.AlbumID = f.AlbumID
		}

		applyNamePoints(&r.Name, &r.Points, f)

		if f.Tags != nil {
			r.Tags = *f.Tags
		}

		if err := validateNamePoints(r.Name, r.Points); err != nil {
			return err
		}

		if r.ArtistID == uuid.Nil {
			return fmt.Errorf("%w: artist_id обязателен", domain.ErrInvalid)
		}

		var err error
		if id == nil {
			result, err = tx.Tracks.Create(ctx, r)
		} else {
			result, err = tx.Tracks.Update(ctx, r)
		}

		return err
	})

	return result, err
}

func applyNamePoints(name *string, points *float64, f CatalogForm) {
	if f.Name != nil {
		*name = strings.TrimSpace(*f.Name)
	}

	if f.Points != nil {
		*points = *f.Points
	}
}

func validateNamePoints(name string, points float64) error {
	if name == "" || utf8.RuneCountInString(name) > 1000 || points < 0 || points > 100 {
		return fmt.Errorf("%w: требуется название до 1000 символов и points от 0 до 100", domain.ErrInvalid)
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
func (a *App) StartParsing(ctx context.Context) (domain.ParseRun, error) {
	if a.token == "" {
		return domain.ParseRun{}, fmt.Errorf("%w: задайте GENIUS_ACCESS_TOKEN", domain.ErrInvalid)
	}

	return a.uow.Runs.Create(ctx)
}

// RetryArtist разрешает повторную обработку при следующем запуске парсинга.
func (a *App) RetryArtist(ctx context.Context, id uuid.UUID) error {
	return a.uow.Artists.Retry(ctx, id)
}
