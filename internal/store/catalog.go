package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// ArtistRepository управляет исполнителями.
type ArtistRepository struct{ db DB }

// List возвращает страницу исполнителей с фильтрацией и сортировкой.
func (r ArtistRepository) List(ctx context.Context, p domain.Page) ([]domain.Artist, error) {
	return query[domain.Artist](ctx, r.db, `SELECT * FROM artists
		WHERE ($3='' OR status=$3) AND ($4='' OR $4=ANY(tags))
		AND ($5='' OR strpos(lower(name),lower($5))>0)
		ORDER BY `+catalogOrder(p, "created_at DESC,id DESC")+` LIMIT $1 OFFSET $2`,
		[]any{p.Limit, p.Offset, p.Status, p.Tag, p.Name})
}

func catalogOrder(p domain.Page, fallback string) string {
	column := map[string]string{
		"name": "lower(name)", "points": "points", "created_at": "created_at",
		"status": "status", "release_date": "release_date",
	}[p.Sort]
	if column == "" {
		return fallback
	}

	if p.Order == "desc" {
		return column + " DESC,id DESC"
	}

	return column + " ASC,id ASC"
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
		genius_id=$5,music_brainz_id=$6,logo_download_id=CASE
			WHEN EXISTS(SELECT 1 FROM downloads WHERE id=artists.logo_download_id AND source_type='upload')
			THEN logo_download_id ELSE COALESCE($7,logo_download_id) END,
		status='parsed',error=$8,lease_until=NULL,updated_at=now() WHERE id=$1`,
		[]any{a.ID, a.FullName, a.Country, a.Artists, a.GeniusID, a.MusicBrainzID, a.LogoDownloadID, a.Error})
}

// SetLogo назначает изображение, загруженное вручную.
func (r ArtistRepository) SetLogo(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, "UPDATE artists SET logo_download_id=$2,updated_at=now() WHERE id=$1", []any{id, downloadID})
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
		ORDER BY `+catalogOrder(p, "created_at,id")+` LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.ArtistID})
}

// Get возвращает альбом по ID.
func (r AlbumRepository) Get(ctx context.Context, id uuid.UUID) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, "SELECT * FROM albums WHERE id=$1", []any{id})
}

// Create добавляет альбом вручную.
func (r AlbumRepository) Create(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `INSERT INTO albums(id,name,artist_id,points,release_date,manual_release_date,tags)
		VALUES($1,$2,$3,$4,$5,$6,COALESCE($7,'{}'::text[])) RETURNING *`,
		[]any{a.ID, a.Name, a.ArtistID, a.Points, a.ReleaseDate, a.ManualReleaseDate, a.Tags})
}

// Update изменяет пользовательские атрибуты альбома.
func (r AlbumRepository) Update(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `UPDATE albums SET name=$2,points=$3,release_date=$4,
		manual_release_date=$5,tags=$6,updated_at=now() WHERE id=$1 RETURNING *`,
		[]any{a.ID, a.Name, a.Points, a.ReleaseDate, a.ManualReleaseDate, a.Tags})
}

// SetCover назначает обложку, загруженную вручную.
func (r AlbumRepository) SetCover(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, "UPDATE albums SET cover_download_id=$2,updated_at=now() WHERE id=$1", []any{id, downloadID})
}

// Import обновляет альбом по ID Genius, не изменяя оценку пользователя.
func (r AlbumRepository) Import(ctx context.Context, a domain.Album) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `INSERT INTO albums
		(id,name,artist_id,genius_id,release_date,track_list,track_list_complete,cover_download_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(genius_id) DO UPDATE SET
		release_date=CASE WHEN albums.manual_release_date THEN albums.release_date ELSE EXCLUDED.release_date END,
		track_list=EXCLUDED.track_list,
		track_list_complete=EXCLUDED.track_list_complete,
		cover_download_id=CASE
			WHEN EXISTS(SELECT 1 FROM downloads WHERE id=albums.cover_download_id AND source_type='upload')
			THEN albums.cover_download_id ELSE COALESCE(EXCLUDED.cover_download_id,albums.cover_download_id) END,
		updated_at=now()
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

const tracksWithStatus = `SELECT t.*,COALESCE(audio.status,'missing') AS status FROM tracks t
	LEFT JOIN LATERAL (
		SELECT d.status FROM track_to_download td JOIN downloads d ON d.id=td.download_id
		WHERE td.track_id=t.id AND d.file_type='mp3'
		ORDER BY CASE d.status WHEN 'finished' THEN 0 WHEN 'processing' THEN 1
			WHEN 'pending' THEN 2 WHEN 'created' THEN 3 WHEN 'errored' THEN 4 ELSE 5 END
		LIMIT 1
	) audio ON true`

// List возвращает страницу треков со статусами аудио.
func (r TrackRepository) List(ctx context.Context, p domain.Page) ([]domain.TrackWithStatus, error) {
	return query[domain.TrackWithStatus](ctx, r.db, `SELECT * FROM (`+tracksWithStatus+`
		WHERE ($3::uuid IS NULL OR t.artist_id=$3) AND ($4::uuid IS NULL OR t.album_id=$4)) catalog
		WHERE ($5='' OR status=$5)
		ORDER BY `+catalogOrder(p, "created_at DESC,id DESC")+` LIMIT $1 OFFSET $2`,
		[]any{p.Limit, p.Offset, p.ArtistID, p.AlbumID, p.Status})
}

// GetWithStatus возвращает трек и состояние аудиозаписей по ID.
func (r TrackRepository) GetWithStatus(ctx context.Context, id uuid.UUID) (domain.TrackWithStatus, error) {
	return one[domain.TrackWithStatus](ctx, r.db, tracksWithStatus+" WHERE t.id=$1", []any{id})
}

// Touch отмечает изменение аудиозаписи трека.
func (r TrackRepository) Touch(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "UPDATE tracks SET updated_at=now() WHERE id=$1", []any{id})
}

// Get возвращает трек по ID.
func (r TrackRepository) Get(ctx context.Context, id uuid.UUID) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, "SELECT * FROM tracks WHERE id=$1", []any{id})
}

// Create добавляет трек, проверяя принадлежность альбома исполнителю.
func (r TrackRepository) Create(ctx context.Context, t domain.Track) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `INSERT INTO tracks(id,name,artist_id,album_id,points,tags)
		SELECT $1,$2,$3,$4,$5,COALESCE($6,'{}'::text[]) WHERE $4::uuid IS NULL
		OR EXISTS(SELECT 1 FROM albums WHERE id=$4 AND artist_id=$3) RETURNING *`,
		[]any{t.ID, t.Name, t.ArtistID, t.AlbumID, t.Points, t.Tags})
}

// Update изменяет название и оценку трека.
func (r TrackRepository) Update(ctx context.Context, t domain.Track) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `UPDATE tracks SET name=$2,points=$3,tags=$4,updated_at=now()
		WHERE id=$1 RETURNING *`, []any{t.ID, t.Name, t.Points, t.Tags})
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
