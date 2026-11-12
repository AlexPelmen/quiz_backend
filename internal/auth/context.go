package auth

import (
	"context"

	"github.com/google/uuid"
)

type userContextKey struct{}

// UserID возвращает идентификатор пользователя, проверенного middleware сессии.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userContextKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
