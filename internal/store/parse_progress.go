package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// ParseEvent описывает шаг обработки в журнале запуска.
type ParseEvent struct {
	ID        int64     `json:"id"`
	Artist    string    `json:"artist"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// AddEvent записывает событие текущего запуска.
func (r RunRepository) AddEvent(ctx context.Context, runID uuid.UUID, event ParseEvent) error {
	return execute(ctx, r.db, `INSERT INTO parse_run_events(run_id,artist,stage,message) VALUES ($1,$2,$3,$4)`,
		[]any{runID, event.Artist, event.Stage, event.Message})
}

// Events возвращает последние события запуска.
func (r RunRepository) Events(ctx context.Context, id uuid.UUID, page domain.Page) ([]ParseEvent, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}

	return query[ParseEvent](ctx, r.db, `SELECT id,artist,stage,message,created_at FROM parse_run_events
        WHERE run_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, []any{id, page.Limit, page.Offset})
}

// QueueCatalogRefresh ставит автоматически обработанных исполнителей в очередь нового запуска.
func (r ArtistRepository) QueueCatalogRefresh(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `UPDATE artists SET status='created',updated_at=now() WHERE status IN ('parsed','errored')`)
	return translate(err)
}

// SaveShortlist заменяет список альбомов по итогам последней обработки.
func (r AlbumRepository) SaveShortlist(ctx context.Context, artist uuid.UUID, albums []uuid.UUID) error {
	if len(albums) != 5 {
		_, err := r.db.Exec(ctx, "DELETE FROM artist_album_shortlists WHERE artist_id=$1", artist)
		return translate(err)
	}

	return execute(ctx, r.db, `INSERT INTO artist_album_shortlists(artist_id,album_ids) VALUES ($1,$2)
        ON CONFLICT(artist_id) DO UPDATE SET album_ids=EXCLUDED.album_ids`, []any{artist, albums})
}

// AlbumCleanupRow описывает альбом вне пятёрки и защиту сохранённого квиза.
type AlbumCleanupRow struct {
	ID        uuid.UUID `json:"id"`
	Album     string    `json:"album"`
	Artist    string    `json:"artist"`
	Protected bool      `json:"protected"`
}

const cleanupCandidates = `
    SELECT al.id,al.name AS album,a.name AS artist,
        EXISTS(SELECT 1 FROM quiz_questions q WHERE q.album_id=al.id) AS protected
    FROM artist_album_shortlists s
    JOIN artists a ON a.id=s.artist_id AND a.status='parsed'
    JOIN artist_to_album owner ON owner.artist_id=s.artist_id
    JOIN albums al ON al.id=owner.album_id
    WHERE NOT (al.id=ANY(s.album_ids))
      AND 5=(SELECT count(*) FROM artist_to_album kept
             WHERE kept.artist_id=s.artist_id AND kept.album_id=ANY(s.album_ids))`

// CleanupPreview показывает альбомы, вышедшие за пределы последней подтверждённой пятёрки.
func (r AlbumRepository) CleanupPreview(ctx context.Context, page domain.Page) ([]AlbumCleanupRow, error) {
	return query[AlbumCleanupRow](ctx, r.db, cleanupCandidates+` ORDER BY a.name,al.name,al.id LIMIT $1 OFFSET $2`,
		[]any{page.Limit, page.Offset})
}

// DeleteCleanupAlbums удаляет только выбранные и всё ещё доступные для очистки записи.
func (r AlbumRepository) DeleteCleanupAlbums(ctx context.Context, ids []uuid.UUID) error {
	if _, err := r.db.Exec(ctx, "LOCK TABLE parse_runs IN SHARE MODE"); err != nil {
		return translate(err)
	}

	result, err := r.db.Exec(ctx, `DELETE FROM albums WHERE id IN (
        SELECT id FROM (`+cleanupCandidates+`) candidates WHERE id=ANY($1::uuid[]) AND NOT protected
    ) AND NOT EXISTS (SELECT 1 FROM parse_runs WHERE status IN ('pending','running','stopping'))`, ids)
	if err != nil {
		return translate(err)
	}

	if result.RowsAffected() != int64(len(ids)) {
		return domain.ErrConflict
	}

	return nil
}
