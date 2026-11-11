package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
	"github.com/google/uuid"
)

// StorageActionResult содержит результат обработки одного файла.
type StorageActionResult struct {
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// StorageAction выполняет операции над выбранными неподключёнными файлами.
func (a *App) StorageAction(ctx context.Context, action string, paths []string) ([]StorageActionResult, error) {
	if (action != "delete" && action != "attach") || len(paths) == 0 || len(paths) > 200 {
		return nil, fmt.Errorf("%w: выберите от 1 до 200 файлов и действие", domain.ErrInvalid)
	}
	settings, err := a.Settings(ctx)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(settings.StorageDir)
	if err != nil {
		return nil, err
	}
	result := make([]StorageActionResult, 0, len(paths))
	seen := map[string]bool{}
	for _, name := range paths {
		row := StorageActionResult{Path: name}
		if seen[name] {
			row.Error = "Файл указан повторно"
		} else {
			seen[name] = true
			if err := a.storageActionOne(ctx, root, name, action, settings); err != nil {
				row.Error = err.Error()
			}
		}
		result = append(result, row)
	}
	return result, nil
}

func storagePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", domain.ErrInvalid
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", domain.ErrInvalid
	}
	for p := path; p != root; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(filepath.Base(p), ".") {
			return "", domain.ErrInvalid
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", domain.ErrInvalid
	}
	return path, nil
}

func orphanInSnapshot(path string, refs []store.StoredReference, questions []uuid.UUID) bool {
	for _, ref := range refs {
		if ref.Used {
			abs, err := filepath.Abs(ref.URI)
			if err != nil || filepath.Clean(abs) == filepath.Clean(path) {
				return false
			}
		}
	}
	if filepath.Base(filepath.Dir(path)) == "clips" {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		for _, id := range questions {
			if strings.HasSuffix(base, id.String()) {
				return false
			}
		}
	}
	return true
}

func (a *App) storageActionOne(ctx context.Context, root, name, action string, settings domain.Settings) error {
	path, err := storagePath(root, name)
	if err != nil {
		return err
	}
	fileType := ""
	if action == "attach" {
		fileType = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		if strings.HasPrefix(name, "music/") {
			if fileType != "mp3" {
				return fmt.Errorf("%w: требуется MP3", domain.ErrInvalid)
			}
			probeCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.TaskTimeoutSeconds)*time.Second)
			defer cancel()
			if _, err := probe(probeCtx, settings.FFprobe, path); err != nil {
				return fmt.Errorf("проверка аудио: %w", err)
			}
		} else {
			fileType, err = checkImage(path)
			if err != nil {
				return fmt.Errorf("проверка изображения: %w", err)
			}
		}
	}
	var moved string
	err = a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		downloads, err := tx.Downloads.LockStorageURI(ctx, path)
		if err != nil {
			return err
		}
		refs, questions, err := tx.Downloads.StorageReferences(ctx)
		if err != nil {
			return err
		}
		if !orphanInSnapshot(path, refs, questions) {
			return fmt.Errorf("%w: файл уже используется", domain.ErrConflict)
		}
		for _, d := range downloads {
			if d.Status != "finished" {
				return fmt.Errorf("%w: загрузка ещё выполняется", domain.ErrConflict)
			}
		}
		if action == "attach" {
			return a.attachStorageFile(ctx, tx, root, path, downloads, fileType)
		}
		moved = filepath.Join(filepath.Dir(path), ".orphan-"+uuid.NewString())
		if err := os.Rename(path, moved); err != nil {
			moved = ""
			return err
		}
		return tx.Downloads.DeleteStorageDownloads(ctx, path)
	})
	if err != nil && moved != "" {
		return errors.Join(err, os.Rename(moved, path))
	}
	if err != nil {
		return err
	}
	if moved != "" {
		if err := os.Remove(moved); err != nil {
			return fmt.Errorf("файл перемещён в %s, но не удалён: %w", moved, err)
		}
	}
	return nil
}

func (a *App) attachStorageFile(ctx context.Context, tx *store.UnitOfWork, root, path string, downloads []domain.Download, fileType string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	kind := ""
	switch {
	case strings.HasPrefix(rel, "music/tracks/"), strings.HasPrefix(rel, "music/clips/"):
		kind = "tracks"
	case strings.HasPrefix(rel, "images/artists/"):
		kind = "artists"
	case strings.HasPrefix(rel, "images/albums/"):
		kind = "albums"
	default:
		return fmt.Errorf("%w: неизвестен тип файла", domain.ErrInvalid)
	}
	if len(downloads) > 1 {
		return fmt.Errorf("%w: несколько загрузок с одним путём", domain.ErrConflict)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if len(base) > 39 {
		if _, err := uuid.Parse(base[len(base)-36:]); err == nil && strings.HasSuffix(base[:len(base)-36], " - ") {
			base = base[:len(base)-39]
		}
	}
	candidates, err := tx.Downloads.StorageCandidates(ctx)
	if err != nil {
		return err
	}
	matches := map[uuid.UUID]store.StorageCandidate{}
	for _, candidate := range candidates {
		if candidate.Kind != kind {
			continue
		}
		normal := mediaName(candidate.Artist, candidate.Title, false)
		preview := mediaName(candidate.Artist, candidate.Title, true)
		match := strings.EqualFold(base, normal) || (kind == "tracks" && strings.EqualFold(base, preview))
		if len(downloads) == 1 {
			meta := downloads[0].Meta
			match = match || (strings.EqualFold(tagValue(meta, "artist"), candidate.Artist) && tagValue(meta, "artist") != "" && (kind == "artists" || strings.EqualFold(tagValue(meta, "title"), candidate.Title)))
		}
		if match {
			matches[candidate.ID] = candidate
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("%w: совпадений в каталоге: %d", domain.ErrConflict, len(matches))
	}
	var selected store.StorageCandidate
	for _, item := range matches {
		selected = item
	}
	var d domain.Download
	if len(downloads) == 1 {
		d = downloads[0]
	} else {
		d, err = tx.Downloads.SaveUpload(ctx, domain.Download{ID: uuid.New(), URI: path, FileType: fileType, Meta: map[string]any{}})
		if err != nil {
			return err
		}
	}
	switch kind {
	case "artists":
		return tx.Artists.SetLogoIfEmpty(ctx, selected.ID, d.ID)
	case "albums":
		return tx.Albums.SetCoverIfEmpty(ctx, selected.ID, d.ID)
	default:
		if strings.HasPrefix(rel, "music/clips/") || strings.EqualFold(base, mediaName(selected.Artist, selected.Title, true)) {
			if err := tx.Downloads.MarkPreview(ctx, d.ID); err != nil {
				return err
			}
		}
		return tx.Downloads.Link(ctx, selected.ID, d.ID)
	}
}
