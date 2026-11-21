package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/store"
)

// StorageSize содержит число файлов и их размер в байтах.
type StorageSize struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// StorageFile описывает файл, не связанный с объектами каталога или квиза.
type StorageFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Kind string `json:"kind"`
}

// StorageReport содержит сводку файлов и список неподключённых файлов.
type StorageReport struct {
	Total   StorageSize            `json:"total"`
	Kinds   map[string]StorageSize `json:"kinds"`
	Orphans []StorageFile          `json:"orphans"`
}

// StorageReport сканирует хранилище, учитывая существующие ссылки на медиа.
func (a *App) StorageReport(ctx context.Context) (StorageReport, error) {
	settings, err := a.Settings(ctx)
	if err != nil {
		return StorageReport{}, err
	}
	refs, questions, err := a.uow.Downloads.StorageReferences(ctx)
	if err != nil {
		return StorageReport{}, err
	}

	if a.objects != nil {
		return a.objectStorageReport(ctx, refs, questions)
	}
	return scanStorage(ctx, settings.StorageDir, refs, questions)
}

func (a *App) objectStorageReport(
	ctx context.Context, refs []store.StoredReference, questions []uuid.UUID,
) (StorageReport, error) {
	report := newStorageReport()
	used := make(map[string]bool, len(refs))

	for _, ref := range refs {
		key, err := a.objects.Key(ref.URI)
		if err != nil {
			return report, err
		}

		used[key] = used[key] || ref.Used
	}

	questionIDs := storageQuestionIDs(questions)
	err := a.objects.List(ctx, func(key string, size int64) {
		report.addFile(key, size, used[key], questionIDs)
	})

	return report, err
}

func scanStorage(ctx context.Context, directory string, refs []store.StoredReference, questions []uuid.UUID) (StorageReport, error) {
	report := newStorageReport()
	root, err := filepath.Abs(directory)
	if err != nil {
		return report, err
	}
	used := make(map[string]bool, len(refs)+len(questions))
	for _, ref := range refs {
		path, err := filepath.Abs(ref.URI)
		if err != nil {
			return report, err
		}
		used[filepath.Clean(path)] = used[filepath.Clean(path)] || ref.Used
	}

	questionIDs := storageQuestionIDs(questions)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		report.addFile(filepath.ToSlash(rel), info.Size(), used[filepath.Clean(path)], questionIDs)
		return nil
	})
	return report, err
}

func newStorageReport() StorageReport {
	return StorageReport{Kinds: map[string]StorageSize{}, Orphans: []StorageFile{}}
}

func storageQuestionIDs(questions []uuid.UUID) map[string]bool {
	ids := make(map[string]bool, len(questions))
	for _, id := range questions {
		ids[id.String()] = true
	}

	return ids
}

func (r *StorageReport) addFile(key string, size int64, used bool, questionIDs map[string]bool) {
	kind := "other"

	for prefix, label := range map[string]string{
		"music/tracks/": "tracks", "music/clips/": "clips",
		"images/albums/": "albums", "images/artists/": "artists", "images/quiz-packs/": "packs",
	} {
		if strings.HasPrefix(key, prefix) {
			kind = label
			break
		}
	}

	if kind == "clips" {
		base := strings.TrimSuffix(path.Base(key), path.Ext(key))
		if len(base) >= 36 {
			used = used || questionIDs[base[len(base)-36:]]
		}
	}

	item := r.Kinds[kind]
	item.Count++
	item.Bytes += size
	r.Kinds[kind] = item
	r.Total.Count++

	r.Total.Bytes += size
	if !used {
		r.Orphans = append(r.Orphans, StorageFile{Path: key, Size: size, Kind: kind})
	}
}
