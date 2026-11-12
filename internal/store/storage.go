package store

import (
	"context"

	"devkeeper.com/quiz/internal/domain"
	"github.com/google/uuid"
)

// StorageCandidate описывает объект каталога для сопоставления файла.
type StorageCandidate struct {
	ID     uuid.UUID `db:"id"`
	Kind   string    `db:"kind"`
	Artist string    `db:"artist"`
	Title  string    `db:"title"`
}

// StorageCandidates возвращает объекты, которым можно назначить файл.
func (r DownloadRepository) StorageCandidates(ctx context.Context) ([]StorageCandidate, error) {
	return query[StorageCandidate](ctx, r.db, `SELECT a.id,'artists' AS kind,a.name AS artist,'' AS title FROM artists a WHERE a.logo_download_id IS NULL
        UNION ALL SELECT al.id,'albums',a.name,al.name FROM albums al JOIN artist_to_album own ON own.album_id=al.id AND own.is_primary JOIN artists a ON a.id=own.artist_id WHERE al.cover_download_id IS NULL
        UNION ALL SELECT t.id,'tracks',a.name,t.name FROM tracks t JOIN artist_to_track own ON own.track_id=t.id JOIN artists a ON a.id=own.artist_id`, nil)
}

// LockStorageURI блокирует операции с одним путём и существующие загрузки.
func (r DownloadRepository) LockStorageURI(ctx context.Context, uri string) ([]domain.Download, error) {
	if _, err := r.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, uri); err != nil {
		return nil, err
	}
	return query[domain.Download](ctx, r.db, `SELECT * FROM downloads WHERE uri=$1 ORDER BY id FOR UPDATE`, []any{uri})
}

// DeleteStorageDownloads удаляет записи загрузок, ссылающиеся на путь.
func (r DownloadRepository) DeleteStorageDownloads(ctx context.Context, uri string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM downloads WHERE uri=$1`, uri)
	return translate(err)
}

// MarkPreview помечает короткую запись перед её привязкой к треку.
func (r DownloadRepository) MarkPreview(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE downloads SET preview=true WHERE id=$1`, []any{id})
}

// SetLogoIfEmpty назначает изображение, если исполнитель ещё не имеет логотипа.
func (r ArtistRepository) SetLogoIfEmpty(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE artists SET logo_download_id=$2,updated_at=now() WHERE id=$1 AND logo_download_id IS NULL`, []any{id, downloadID})
}

// SetCoverIfEmpty назначает изображение, если альбом ещё не имеет обложки.
func (r AlbumRepository) SetCoverIfEmpty(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE albums SET cover_download_id=$2,updated_at=now() WHERE id=$1 AND cover_download_id IS NULL`, []any{id, downloadID})
}

// StoredReference описывает путь загрузки и наличие использующих её объектов.
type StoredReference struct {
	URI  string `db:"uri"`
	Used bool   `db:"used"`
}

// DownloadNames возвращает названия объекта и исполнителя, связанных с загрузкой.
func (r DownloadRepository) DownloadNames(ctx context.Context, id uuid.UUID) (string, string, error) {
	var artist, title string
	err := r.db.QueryRow(ctx, `SELECT COALESCE(
        (SELECT a.name FROM track_to_download td JOIN artist_to_track own ON own.track_id=td.track_id AND own.is_primary
         JOIN artists a ON a.id=own.artist_id WHERE td.download_id=$1 ORDER BY td.track_id LIMIT 1),
        (SELECT a.name FROM artists a WHERE a.logo_download_id=$1 ORDER BY a.id LIMIT 1),
        (SELECT a.name FROM albums al JOIN artist_to_album own ON own.album_id=al.id AND own.is_primary JOIN artists a ON a.id=own.artist_id
         WHERE al.cover_download_id=$1 ORDER BY al.id LIMIT 1), ''), COALESCE(
        (SELECT t.name FROM track_to_download td JOIN tracks t ON t.id=td.track_id WHERE td.download_id=$1 ORDER BY t.id LIMIT 1),
        (SELECT al.name FROM albums al WHERE al.cover_download_id=$1 ORDER BY al.id LIMIT 1), '')`, id).Scan(&artist, &title)
	return artist, title, err
}

// StorageReferences возвращает пути загрузок и идентификаторы вопросов с кэшированным аудио.
func (r DownloadRepository) StorageReferences(ctx context.Context) ([]StoredReference, []uuid.UUID, error) {
	refs, err := query[StoredReference](ctx, r.db, `SELECT d.uri,
        EXISTS(SELECT 1 FROM track_to_download t WHERE t.download_id=d.id)
        OR EXISTS(SELECT 1 FROM artists a WHERE a.logo_download_id=d.id)
        OR EXISTS(SELECT 1 FROM albums a WHERE a.cover_download_id=d.id)
        OR EXISTS(SELECT 1 FROM quiz_packs p WHERE p.cover_download_id=d.id)
        OR EXISTS(SELECT 1 FROM quiz_questions q WHERE q.image_download_id=d.id OR q.sound_download_id=d.id)
        OR EXISTS(SELECT 1 FROM album_guessing g WHERE g.cover_download_id=d.id) AS used
        FROM downloads d WHERE d.uri<>''`, nil)
	if err != nil {
		return nil, nil, err
	}
	questions, err := query[struct{ ID uuid.UUID }](ctx, r.db, `SELECT id FROM quiz_questions WHERE sound_download_id IS NOT NULL`, nil)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]uuid.UUID, 0, len(questions))
	for _, q := range questions {
		ids = append(ids, q.ID)
	}
	return refs, ids, nil
}
