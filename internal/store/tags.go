package store

import (
	"context"

	"github.com/google/uuid"
)

// Tag — элемент справочника тегов.
type Tag struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// TagRepository хранит справочник тегов.
type TagRepository struct{ db DB }

// List возвращает все теги по имени.
func (r TagRepository) List(ctx context.Context) ([]Tag, error) {
	return query[Tag](ctx, r.db, `SELECT id, name FROM tags ORDER BY name`, nil)
}

// Create добавляет тег.
func (r TagRepository) Create(ctx context.Context, name string) (Tag, error) {
	return one[Tag](ctx, r.db, `INSERT INTO tags(name) VALUES($1) RETURNING id, name`, []any{name})
}

// Update переименовывает тег и связанные метки.
func (r TagRepository) Update(ctx context.Context, id uuid.UUID, name string) (Tag, error) {
	return one[Tag](ctx, r.db, `UPDATE tags SET name=$2 WHERE id=$1 RETURNING id, name`, []any{id, name})
}

// Delete удаляет неиспользуемый тег.
func (r TagRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, `DELETE FROM tags WHERE id=$1`, []any{id})
}
