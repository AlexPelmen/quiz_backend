package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
)

// GeniusQuota возвращает последний известный лимит без запроса к Genius.
func (a *App) GeniusQuota(ctx context.Context) (*domain.GeniusQuota, error) {
	q, err := a.uow.Quota.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &q, nil
}

// RefreshGeniusQuota запрашивает текущие заголовки лимита Genius.
func (a *App) RefreshGeniusQuota(ctx context.Context) (*domain.GeniusQuota, error) {
	if a.token == "" {
		return nil, fmt.Errorf("%w: задайте GENIUS_ACCESS_TOKEN", domain.ErrInvalid)
	}

	s, err := a.Settings(ctx)
	if err != nil {
		return nil, err
	}

	g := source.NewGenius(s, a.token)
	observed := false

	g.SetQuotaObserver(func(ctx context.Context, quota domain.GeniusQuota) error {
		if err := a.uow.Quota.Save(ctx, quota); err != nil {
			return err
		}

		observed = true

		return nil
	})

	if err := g.RefreshQuota(ctx); err != nil {
		var apiErr *source.HTTPError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || !observed {
			return nil, err
		}
	}

	if !observed {
		return nil, errors.New("источник Genius не вернул заголовки лимита")
	}

	return a.GeniusQuota(ctx)
}
