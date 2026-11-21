package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// WithStorageLock удерживает блокировку ссылки между короткими транзакциями и операциями S3.
func (u *UnitOfWork) WithStorageLock(ctx context.Context, uri string, fn func(*UnitOfWork) error) (result error) {
	conn, err := u.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	var locked bool

	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", uri).Scan(&locked)
	if err != nil {
		return errors.Join(err, closeStorageConn(ctx, conn.Hijack()))
	}

	if !locked {
		return fmt.Errorf("%w: файл уже обрабатывается", domain.ErrConflict)
	}

	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", uri); err != nil {
			result = errors.Join(result, err, closeStorageConn(ctx, conn.Hijack()))
		}
	}()

	lockedUOW := bind(conn)
	lockedUOW.begin = conn.BeginTx

	return fn(lockedUOW)
}

func closeStorageConn(ctx context.Context, conn *pgx.Conn) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return conn.Close(cleanup)
}

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

// HasObjectLocations проверяет, что каталог уже использует ссылки S3.
func (r DownloadRepository) HasObjectLocations(ctx context.Context) (bool, error) {
	var exists bool

	err := r.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM downloads WHERE starts_with(uri, 's3://'))").Scan(&exists)

	return exists, err
}

// CountOutsideStorage считает сохранённые ссылки вне заданного хранилища.
func (r DownloadRepository) CountOutsideStorage(ctx context.Context, prefix string) (int, error) {
	var count int

	err := r.db.QueryRow(ctx, `
		SELECT count(*) FROM downloads WHERE uri<>'' AND NOT starts_with(uri, $1)`, prefix).Scan(&count)

	return count, err
}

// ReplaceMediaLocations атомарно заменяет проверенные ссылки при остановленных работниках.
func (u *UnitOfWork) ReplaceMediaLocations(
	ctx context.Context, mapping map[string]string, prefix string, expected int,
) error {
	oldURIs, newURIs := make([]string, 0, len(mapping)), make([]string, 0, len(mapping))
	for oldURI, newURI := range mapping {
		oldURIs, newURIs = append(oldURIs, oldURI), append(newURIs, newURI)
	}

	return u.Transaction(ctx, func(tx *UnitOfWork) error {
		if _, err := tx.db.Exec(ctx, "LOCK TABLE downloads IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return err
		}

		tag, err := tx.db.Exec(ctx, `UPDATE downloads AS d SET uri=m.new_uri
			FROM unnest($1::text[], $2::text[]) AS m(old_uri, new_uri) WHERE d.uri=m.old_uri`, oldURIs, newURIs)
		if err != nil {
			return err
		}

		outside, err := tx.Downloads.CountOutsideStorage(ctx, prefix)
		if err != nil {
			return err
		}

		if outside != 0 || tag.RowsAffected() != int64(expected) {
			return errors.New("состав ссылок изменился после проверки, изменения отменены")
		}

		return nil
	})
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
