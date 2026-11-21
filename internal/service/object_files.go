package service

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// OpenObject запрашивает поток для ссылки S3, сохранённой в каталоге.
func (a *App) OpenObject(ctx context.Context, uri string, headers http.Header) (*s3.GetObjectOutput, error) {
	if a.objects == nil {
		return nil, errors.New("в каталоге есть ссылка S3, но режим S3 отключён")
	}

	key, err := a.objects.Key(uri)
	if err != nil {
		return nil, err
	}

	return a.objects.Get(ctx, key, headers)
}

type mediaUpload struct {
	resource, name string
	body           io.Reader
}

func (a *App) stageMediaUpload(ctx context.Context, s domain.Settings, input mediaUpload) (domain.Download, error) {
	if a.objects != nil {
		work, err := os.MkdirTemp("", "quiz-upload-")
		if err != nil {
			return domain.Download{}, err
		}

		defer a.removeWork(ctx, work)

		s.StorageDir = work
	}

	d, err := stageUpload(ctx, s, input.resource, input.body)
	if err != nil {
		return d, err
	}

	if a.objects != nil {
		dir, err := filepath.Rel(s.StorageDir, filepath.Dir(d.URI))
		if err != nil {
			return d, err
		}

		key := mediaObjectKey(filepath.ToSlash(dir), input.name, d.FileType, d.ID)
		d.URI, err = a.publishObject(ctx, key, d.URI)

		return d, err
	}

	if input.name != "" {
		published, err := linkMediaFile(d.URI, filepath.Dir(d.URI), input.name, d.FileType, d.ID)
		if err != nil {
			a.cleanupUpload(ctx, d)
			return d, err
		}

		d.URI = published
	}

	return d, nil
}

func mediaObjectKey(dir, name, extension string, id uuid.UUID) string {
	base := id.String()
	if name != "" {
		base = name + " - " + base
	}

	return path.Join(dir, base+"."+extension)
}

func (a *App) publishObject(ctx context.Context, key, filename string) (string, error) {
	if err := a.objects.PutFile(ctx, key, filename, nil); err != nil {
		return "", err
	}

	return a.objects.URI(key), nil
}

func (a *App) removeMedia(ctx context.Context, uri string) error {
	if a.objects == nil {
		return os.Remove(uri)
	}

	key, err := a.objects.Key(uri)
	if err != nil {
		return err
	}

	return a.objects.Delete(ctx, key)
}

func (a *App) removeWork(ctx context.Context, dir string) {
	if err := os.RemoveAll(dir); err != nil {
		a.log.WarnContext(ctx, "удаление временных файлов медиа", "directory", dir, "error", err)
	}
}

func (a *App) objectClip(ctx context.Context, s domain.Settings, in clipInput) (string, error) {
	q, d := in.Question, in.Download
	key := "music/clips/" + q.ID.String() + ".mp3"

	candidates := []string{key}
	if name := mediaName(in.Artist, in.Title, true); name != "" {
		candidates = append(candidates, "music/clips/"+name+" - "+q.ID.String()+".mp3")
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	for _, candidate := range candidates {
		_, err := a.objects.Head(ctx, candidate)
		if err == nil {
			return a.objects.URI(candidate), nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}

	source, err := a.objects.Key(d.URI)
	if err != nil {
		return "", err
	}

	work, err := os.MkdirTemp("", "quiz-clip-")
	if err != nil {
		return "", err
	}

	defer a.removeWork(ctx, work)

	input, output := filepath.Join(work, "source.mp3"), filepath.Join(work, "clip.mp3")
	if err := a.objects.SaveLocal(ctx, source, input, s.MaxDownloadBytes); err != nil {
		return "", err
	}

	if err := in.render(ctx, s.FFmpeg, input, output); err != nil {
		return "", err
	}

	if err := a.objects.PutFile(ctx, key, output, nil); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}

	return a.objects.URI(key), nil
}

func (a *App) downloadLocation(uri string) (string, error) {
	if a.objects != nil {
		if _, err := a.objects.Key(uri); err != nil {
			return "", err
		}
	} else if strings.HasPrefix(uri, "s3://") {
		return "", errors.New("в каталоге есть ссылка S3, но режим S3 отключён")
	}

	return uri, nil
}
