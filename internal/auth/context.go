package auth

import (
	"context"

	"github.com/google/uuid"
)

type userContextKey struct{}

// UserID возвращает идентификатор пользователя, проверенного middleware сессии.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user.ID, ok && user.ID != uuid.Nil
}

// ReadyPacksOnly ограничивает игрока готовыми пакетами.
func ReadyPacksOnly(ctx context.Context) bool {
	user, ok := ctx.Value(userContextKey{}).(User)
	return ok && !user.Can("packs")
}
