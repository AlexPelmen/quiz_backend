package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// ArtistRepository управляет исполнителями.
type ArtistRepository struct{ db DB }

// List возвращает страницу исполнителей от новых к старым с фильтрацией.
func (r ArtistRepository) List(ctx context.Context, p domain.Page) ([]domain.Artist, error) {
	return query[domain.Artist](ctx, r.db, `SELECT * FROM artists
		WHERE ($3='' OR status=$3) AND ($4='' OR $4=ANY(tags))
		ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.Status, p.Tag})
}

// Get возвращает исполнителя по ID.
func (r ArtistRepository) Get(ctx context.Context, id uuid.UUID) (domain.Artist, error) {
	return one[domain.Artist](ctx, r.db, "SELECT * FROM artists WHERE id=$1", []any{id})
}

// Create добавляет исполнителя в очередь будущего запуска парсинга.
func (r ArtistRepository) Create(ctx context.Context, a domain.Artist) (domain.Artist, error) {
	return one[domain.Artist](ctx, r.db, `INSERT INTO artists(id,name,tags,points)
		VALUES($1,$2,$3,$4) RETURNING *`, []any{a.ID, a.Name, a.Tags, a.Points})
}

// Update сохраняет редактируемые пользователем поля.
func (r ArtistRepository) Update(ctx context.Context, a domain.Artist) (domain.Artist, error) {
	return one[domain.Artist](ctx, r.db, `UPDATE artists SET name=$2,tags=$3,points=$4,
		full_name=$5,country=$6,artists=$7,manual_fields=$8,updated_at=now() WHERE id=$1 RETURNING *`,
		[]any{a.ID, a.Name, a.Tags, a.Points, a.FullName, a.Country, a.Artists, a.ManualFields})
}

// Delete удаляет исполнителя, если он не используется сохранённым пакетом.
func (r ArtistRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM artists WHERE id=$1", []any{id})
}

// Retry возвращает исполнителя в очередь явного парсинга.
func (r ArtistRepository) Retry(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE artists SET status='created',error='',updated_at=now()
		WHERE id=$1 AND status IN ('errored','parsed')`, []any{id})
}

// Claim атомарно захватывает следующего исполнителя на ограниченный срок.
func (r ArtistRepository) Claim(ctx context.Context, seconds int, startedAt time.Time) (domain.Artist, error) {
	return one[domain.Artist](ctx, r.db, `UPDATE artists SET status='processing',
		lease_until=now()+make_interval(secs=>$1),updated_at=now()
		WHERE id=(SELECT id FROM artists WHERE status='created' OR (status='errored' AND updated_at<$2)
		OR (status='processing' AND lease_until<now()) ORDER BY created_at
		FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, []any{seconds, startedAt})
}

// SetStatus меняет состояние исполнителя, которого сейчас не обрабатывает парсер.
func (r ArtistRepository) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	return execute(ctx, r.db, `UPDATE artists SET status=$2,updated_at=now()
		WHERE id=$1 AND status<>'processing'`, []any{id, status})
}

// Imported обновляет данные источников, сохраняя пользовательские исправления.
func (r ArtistRepository) Imported(ctx context.Context, a domain.Artist) error {
	return execute(ctx, r.db, `UPDATE artists SET
		full_name=CASE WHEN 'full_name'=ANY(manual_fields) THEN full_name ELSE $2 END,
		country=CASE WHEN 'country'=ANY(manual_fields) THEN country ELSE $3 END,
		artists=CASE WHEN 'artists'=ANY(manual_fields) THEN artists ELSE $4 END,
		genius_id=$5,music_brainz_id=$6,logo_download_id=COALESCE($7,logo_download_id),
		status='parsed',error=$8,lease_until=NULL,updated_at=now() WHERE id=$1`,
		[]any{a.ID, a.FullName, a.Country, a.Artists, a.GeniusID, a.MusicBrainzID, a.LogoDownloadID, a.Error})
}

// Fail сохраняет диагностическую ошибку импорта исполнителя.
func (r ArtistRepository) Fail(ctx context.Context, id uuid.UUID, message string) error {
	return execute(ctx, r.db, `UPDATE artists SET status='errored',error=$2,
		lease_until=NULL,updated_at=now() WHERE id=$1`, []any{id, message})
}

// AlbumRepository управляет альбомами.
type AlbumRepository struct{ db DB }

// List возвращает страницу альбомов.
func (r AlbumRepository) List(ctx context.Context, p domain.Page) ([]domain.Album, error) {
	return query[domain.Album](ctx, r.db, `SELECT * FROM albums WHERE ($3::uuid IS NULL OR artist_id=$3)
		ORDER BY created_at,id LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.ArtistID})
}

// Get возвращает альбом по ID.
func (r AlbumRepository) Get(ctx context.Context, id uuid.UUID) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, "SELECT * FROM albums WHERE id=$1", []any{id})
}

// Create добавляет альбом вручную.
func (r AlbumRepository) Create(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `INSERT INTO albums(id,name,artist_id,points,release_date)
		VALUES($1,$2,$3,$4,$5) RETURNING *`, []any{a.ID, a.Name, a.ArtistID, a.Points, a.ReleaseDate})
}

// Update изменяет пользовательские атрибуты альбома.
func (r AlbumRepository) Update(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `UPDATE albums SET name=$2,points=$3,release_date=$4,
		updated_at=now() WHERE id=$1 RETURNING *`, []any{a.ID, a.Name, a.Points, a.ReleaseDate})
}

// Import обновляет альбом по ID Genius, не изменяя оценку пользователя.
func (r AlbumRepository) Import(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `INSERT INTO albums
		(id,name,artist_id,genius_id,release_date,track_list,track_list_complete,cover_download_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(genius_id) DO UPDATE SET
		release_date=EXCLUDED.release_date,track_list=EXCLUDED.track_list,
		track_list_complete=EXCLUDED.track_list_complete,
		cover_download_id=COALESCE(EXCLUDED.cover_download_id,albums.cover_download_id),updated_at=now()
		RETURNING *`, []any{
		a.ID, a.Name, a.ArtistID, a.GeniusID, a.ReleaseDate,
		a.TrackList, a.TrackListComplete, a.CoverDownloadID,
	})
}

// Delete удаляет альбом, сохраняя его треки без привязки к альбому.
func (r AlbumRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM albums WHERE id=$1", []any{id})
}

// TrackRepository управляет отдельными импортированными треками.
type TrackRepository struct{ db DB }

// List возвращает страницу треков.
func (r TrackRepository) List(ctx context.Context, p domain.Page) ([]domain.Track, error) {
	return query[domain.Track](ctx, r.db, `SELECT * FROM tracks
		WHERE ($3::uuid IS NULL OR artist_id=$3) AND ($4::uuid IS NULL OR album_id=$4)
		ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.ArtistID, p.AlbumID})
}

// Get возвращает трек по ID.
func (r TrackRepository) Get(ctx context.Context, id uuid.UUID) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, "SELECT * FROM tracks WHERE id=$1", []any{id})
}

// Create добавляет трек, проверяя принадлежность альбома исполнителю.
func (r TrackRepository) Create(ctx context.Context, t domain.Track) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `INSERT INTO tracks(id,name,artist_id,album_id,points)
		SELECT $1,$2,$3,$4,$5 WHERE $4::uuid IS NULL
		OR EXISTS(SELECT 1 FROM albums WHERE id=$4 AND artist_id=$3) RETURNING *`,
		[]any{t.ID, t.Name, t.ArtistID, t.AlbumID, t.Points})
}

// Update изменяет название и оценку трека.
func (r TrackRepository) Update(ctx context.Context, t domain.Track) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `UPDATE tracks SET name=$2,points=$3,updated_at=now()
		WHERE id=$1 RETURNING *`, []any{t.ID, t.Name, t.Points})
}

// Import сохраняет трек по ID Genius без изменения пользовательской оценки.
func (r TrackRepository) Import(ctx context.Context, t domain.Track) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `INSERT INTO tracks(id,name,artist_id,album_id,genius_id)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(genius_id) DO UPDATE SET
		album_id=EXCLUDED.album_id,updated_at=now() RETURNING *`,
		[]any{t.ID, t.Name, t.ArtistID, t.AlbumID, t.GeniusID})
}

// Delete удаляет трек, не используемый вопросами.
func (r TrackRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM tracks WHERE id=$1", []any{id})
}
