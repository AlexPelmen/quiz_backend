package store

import (
	"context"

	"devkeeper.com/quiz/internal/domain"
)

// QuotaRepository хранит последнее наблюдение лимита Genius.
type QuotaRepository struct{ db DB }

// Get возвращает сохранённый лимит без нового запроса к Genius.
func (r QuotaRepository) Get(ctx context.Context) (domain.GeniusQuota, error) {
	return one[domain.GeniusQuota](ctx, r.db,
		"SELECT quota_limit,remaining,reset_at,observed_at FROM genius_quota WHERE id=true", nil)
}

// Save сохраняет только наиболее свежий ответ API.
func (r QuotaRepository) Save(ctx context.Context, q domain.GeniusQuota) error {
	_, err := r.db.Exec(ctx, `INSERT INTO genius_quota(id,quota_limit,remaining,reset_at,observed_at)
		VALUES(true,$1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET
		quota_limit=EXCLUDED.quota_limit,remaining=EXCLUDED.remaining,
		reset_at=EXCLUDED.reset_at,observed_at=EXCLUDED.observed_at
		WHERE genius_quota.observed_at<=EXCLUDED.observed_at`, q.Limit, q.Remaining, q.ResetAt, q.ObservedAt)

	return err
}
