package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// Tags возвращает справочник тегов.
func (a *App) Tags(ctx context.Context) ([]store.Tag, error) { return a.uow.Tags.List(ctx) }

// SaveTag создаёт или переименовывает тег.
func (a *App) SaveTag(ctx context.Context, id *uuid.UUID, name string) (store.Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return store.Tag{}, fmt.Errorf("%w: название тега должно содержать от 1 до 100 символов", domain.ErrInvalid)
	}

	if id == nil {
		return a.uow.Tags.Create(ctx, name)
	}

	return a.uow.Tags.Update(ctx, *id, name)
}

// DeleteTag удаляет неиспользуемый тег.
func (a *App) DeleteTag(ctx context.Context, id uuid.UUID) error {
	err := a.uow.Tags.Delete(ctx, id)
	if errors.Is(err, domain.ErrConflict) {
		return fmt.Errorf("%w: тег используется в каталоге или пакете", domain.ErrConflict)
	}

	return err
}
