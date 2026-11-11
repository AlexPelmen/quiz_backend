package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"devkeeper.com/quiz/internal/store"
	"github.com/google/uuid"
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
	return scanStorage(ctx, settings.StorageDir, refs, questions)
}

func scanStorage(ctx context.Context, directory string, refs []store.StoredReference, questions []uuid.UUID) (StorageReport, error) {
	report := StorageReport{Kinds: map[string]StorageSize{}, Orphans: []StorageFile{}}
	root, err := filepath.Abs(directory)
	if err != nil {
		return report, err
	}
	used := make(map[string]bool, len(refs)+len(questions))
	questionIDs := make(map[string]bool, len(questions))
	for _, ref := range refs {
		path, err := filepath.Abs(ref.URI)
		if err != nil {
			return report, err
		}
		used[filepath.Clean(path)] = used[filepath.Clean(path)] || ref.Used
	}
	for _, id := range questions {
		questionIDs[id.String()] = true
		used[filepath.Join(root, "music", "clips", id.String()+".mp3")] = true
	}
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
		rel = filepath.ToSlash(rel)
		kind := "other"
		for prefix, label := range map[string]string{"music/tracks/": "tracks", "music/clips/": "clips", "images/albums/": "albums", "images/artists/": "artists", "images/quiz-packs/": "packs"} {
			if strings.HasPrefix(rel, prefix) {
				kind = label
				break
			}
		}
		item := report.Kinds[kind]
		item.Count++
		item.Bytes += info.Size()
		report.Kinds[kind] = item
		report.Total.Count++
		report.Total.Bytes += info.Size()
		clipUsed := false
		if kind == "clips" {
			base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if len(base) >= 36 {
				clipUsed = questionIDs[base[len(base)-36:]]
			}
		}
		if !used[filepath.Clean(path)] && !clipUsed {
			report.Orphans = append(report.Orphans, StorageFile{Path: rel, Size: info.Size(), Kind: kind})
		}
		return nil
	})
	return report, err
}
