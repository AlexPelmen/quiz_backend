package service

import (
	"context"
	"devkeeper.com/quiz/internal/domain"
)

// CatalogCount возвращает общее число записей каталога.
func (a *App) ListCount(ctx context.Context, resource string, p domain.Page) (int64, error) {
	return a.uow.ListCount(ctx, resource, p)
}

// DownloadActiveCount возвращает число активных заданий указанного типа.
func (a *App) DownloadActiveCount(ctx context.Context, kind string) (int64, error) {
	return a.uow.DownloadActiveCount(ctx, kind)
}
