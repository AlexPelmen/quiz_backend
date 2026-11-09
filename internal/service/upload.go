package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// Upload проверяет файл и назначает его изображением или основной записью каталога.
func (a *App) Upload(ctx context.Context, resource string, id uuid.UUID, input io.Reader) (domain.Download, error) {
	if _, err := a.readOne(ctx, resource, id); err != nil {
		return domain.Download{}, err
	}

	settings, err := a.Settings(ctx)
	if err != nil {
		return domain.Download{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(settings.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	d, err := stageUpload(ctx, settings, resource, input)
	if err != nil {
		return domain.Download{}, err
	}

	saved, err := a.saveUpload(ctx, resource, id, d)
	if err != nil {
		a.cleanupUpload(ctx, d)
	}

	return saved, err
}

func stageUpload(
	ctx context.Context, settings domain.Settings, resource string, input io.Reader,
) (d domain.Download, result error) {
	directory, ok := map[string]string{
		"artists": "images/artists", "albums": "images/albums", "tracks": "music/tracks",
		"quiz-packs": "images/quiz-packs",
	}[resource]
	if !ok {
		return d, domain.ErrNotFound
	}

	dir, err := filepath.Abs(filepath.Join(settings.StorageDir, filepath.FromSlash(directory)))
	if err != nil {
		return d, err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return d, err
	}

	temp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return d, err
	}

	defer func() {
		if err := os.Remove(temp.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()

	limit := min(settings.MaxDownloadBytes, 100<<20)
	n, err := io.Copy(temp, io.LimitReader(input, limit+1))

	if err := errors.Join(err, temp.Close()); err != nil {
		return d, err
	}

	if n == 0 || n > limit {
		return d, fmt.Errorf("%w: размер файла должен быть от 1 до %d байт", domain.ErrInvalid, limit)
	}

	d = domain.Download{ID: uuid.New(), Meta: map[string]any{}}

	if resource == "tracks" {
		duration, err := probe(ctx, settings.FFprobe, temp.Name())
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) || errors.Is(err, errInvalidMP3) {
				return d, fmt.Errorf("%w: требуется читаемый MP3-файл", domain.ErrInvalid)
			}

			return d, err
		}

		d.FileType = "mp3"
		d.Meta["duration_seconds"] = duration
	} else {
		kind, err := checkImage(temp.Name())
		if err != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				return d, err
			}

			return d, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
		}

		d.FileType = kind
	}

	d.URI = filepath.Join(dir, d.ID.String()+"."+d.FileType)

	return d, os.Rename(temp.Name(), d.URI)
}

func (a *App) saveUpload(
	ctx context.Context, resource string, id uuid.UUID, d domain.Download,
) (domain.Download, error) {
	var saved domain.Download

	err := a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		var err error

		saved, err = tx.Downloads.SaveUpload(ctx, d)
		if err != nil {
			return err
		}

		switch resource {
		case "artists":
			return tx.Artists.SetLogo(ctx, id, d.ID)
		case "albums":
			return tx.Albums.SetCover(ctx, id, d.ID)
		default:
			if err := tx.Tracks.Touch(ctx, id); err != nil {
				return err
			}

			if err := tx.Downloads.Link(ctx, id, d.ID); err != nil {
				return err
			}

			if seconds, ok := d.Meta["duration_seconds"].(float64); ok && seconds <= math.MaxInt32 {
				return tx.Downloads.FillTrackDuration(ctx, d.ID, max(1, int(math.Round(seconds))))
			}

			return nil
		}
	})

	return saved, err
}

func (a *App) cleanupUpload(ctx context.Context, d domain.Download) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// При обрыве соединения во время commit сначала проверяем, видна ли запись.
	if _, err := a.uow.Downloads.Get(ctx, d.ID); !errors.Is(err, domain.ErrNotFound) {
		if err != nil {
			a.log.Error("проверка записи загруженного файла", "download_id", d.ID, "error", err)
		}

		return
	}

	if err := os.Remove(d.URI); err != nil {
		a.log.Error("удаление непривязанного файла", "download_id", d.ID, "error", err)
	}
}
