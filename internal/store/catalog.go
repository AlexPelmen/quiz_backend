package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// ArtistRepository управляет исполнителями.
type ArtistRepository struct{ db DB }

// List возвращает страницу исполнителей с фильтрацией и сортировкой.
func (r ArtistRepository) List(ctx context.Context, p domain.Page) ([]domain.Artist, error) {
	return query[domain.Artist](ctx, r.db, `SELECT * FROM catalog_artists
		WHERE ($3='' OR status=$3) AND ($4='' OR $4=ANY(tags))
		AND ($5='' OR strpos(lower(name),lower($5))>0)
		ORDER BY `+catalogOrder(p, "created_at DESC,id DESC")+` LIMIT $1 OFFSET $2`,
		[]any{p.Limit, p.Offset, p.Status, p.Tag, p.Name})
}

func catalogOrder(p domain.Page, fallback string) string {
	column := map[string]string{
		"name": "lower(name)", "yt_plays": "yt_plays", "audio_status": "audio_status", "created_at": "created_at",
		"status": "status", "release_date": "release_date",
	}[p.Sort]
	if column == "" {
		return fallback
	}

	if p.Order == "desc" {
		return column + " DESC NULLS LAST,id DESC"
	}

	return column + " ASC NULLS LAST,id ASC"
}

// Get возвращает исполнителя по ID.
func (r ArtistRepository) Get(ctx context.Context, id uuid.UUID) (domain.Artist, error) {
	return one[domain.Artist](ctx, r.db, "SELECT * FROM catalog_artists WHERE id=$1", []any{id})
}

// Create добавляет исполнителя в очередь будущего запуска парсинга.
func (r ArtistRepository) Create(ctx context.Context, a domain.Artist) (domain.Artist, error) {
	err := execute(ctx, r.db, `INSERT INTO artists(id,name,tags,excluded_from_quiz,full_name,country,
        artists,manual_fields,yt_plays,yt_subs,members_known)
        VALUES($1,$2,COALESCE($3::text[],'{}'),$4,$5,$6,COALESCE($7::text[],'{}'),
            COALESCE($8::text[],'{}'),$9,$10,$11)`,
		[]any{
			a.ID, a.Name, a.Tags, a.ExcludedFromQuiz, a.FullName, a.Country, a.Artists, a.ManualFields,
			a.YtPlays, a.YtSubs, a.MembersKnown,
		})
	if err != nil {
		return a, err
	}

	return r.Get(ctx, a.ID)
}

// Update сохраняет редактируемые пользователем поля.
func (r ArtistRepository) Update(ctx context.Context, a domain.Artist) (domain.Artist, error) {
	err := execute(ctx, r.db, `UPDATE artists SET name=$2,tags=$3,excluded_from_quiz=$4,
        full_name=$5,country=$6,artists=$7,manual_fields=$8,yt_plays=$9,yt_subs=$10,members_known=$11,
        status=CASE WHEN status='manual' THEN 'parsed' ELSE status END,updated_at=now() WHERE id=$1`,
		[]any{
			a.ID, a.Name, a.Tags, a.ExcludedFromQuiz, a.FullName, a.Country, a.Artists, a.ManualFields,
			a.YtPlays, a.YtSubs, a.MembersKnown,
		})
	if err != nil {
		return a, err
	}

	return r.Get(ctx, a.ID)
}

// Delete удаляет исполнителя и его каталог в транзакции вызывающего сервиса.
func (r ArtistRepository) Delete(ctx context.Context, id uuid.UUID) error {
	for _, sql := range []string{
		"DELETE FROM tracks WHERE id IN (SELECT track_id FROM artist_to_track WHERE artist_id=$1)",
		"DELETE FROM albums WHERE id IN (SELECT album_id FROM artist_to_album WHERE artist_id=$1)",
	} {
		if _, err := r.db.Exec(ctx, sql, id); err != nil {
			return translate(err)
		}
	}

	return execute(ctx, r.db, "DELETE FROM artists WHERE id=$1", []any{id})
}

// Retry возвращает исполнителя в очередь явного парсинга.
func (r ArtistRepository) Retry(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE artists SET status='created',error='',updated_at=now()
		WHERE id=$1 AND status IN ('errored','parsed','manual')`, []any{id})
}

// Release возвращает незавершённую обработку исполнителя в очередь.
func (r ArtistRepository) Release(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE artists SET status='created',lease_until=NULL,updated_at=now()
		WHERE id=$1 AND status='processing'`, []any{id})
}

// Renew продлевает захват исполнителя во время ожидания повтора.
func (r ArtistRepository) Renew(ctx context.Context, id uuid.UUID, seconds int) error {
	return execute(ctx, r.db, `UPDATE artists SET lease_until=now()+make_interval(secs=>$2)
		WHERE id=$1 AND status='processing'`, []any{id, seconds})
}

// Claim атомарно захватывает следующего исполнителя на ограниченный срок.
func (r ArtistRepository) Claim(ctx context.Context, seconds int, startedAt time.Time) (domain.Artist, error) {
	id, err := one[struct{ ID uuid.UUID }](ctx, r.db, `UPDATE artists SET status='processing',
        lease_until=now()+make_interval(secs=>$1),updated_at=now()
        WHERE id=(SELECT id FROM artists WHERE status='created' OR (status='errored' AND updated_at<$2)
        OR (status='processing' AND lease_until<now()) ORDER BY created_at
        FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id`, []any{seconds, startedAt})
	if err != nil {
		return domain.Artist{}, err
	}

	return r.Get(ctx, id.ID)
}

// SetStatus меняет состояние исполнителя, которого сейчас не обрабатывает парсер.
func (r ArtistRepository) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	return execute(ctx, r.db, `UPDATE artists SET status=$2,updated_at=now()
		WHERE id=$1 AND status<>'processing'`, []any{id, status})
}

// Imported обновляет данные источников, сохраняя пользовательские исправления.
func (r ArtistRepository) Imported(ctx context.Context, a domain.Artist) error {
	return execute(ctx, r.db, `UPDATE artists SET
		full_name=CASE WHEN 'full_name'=ANY(manual_fields) THEN full_name ELSE COALESCE(NULLIF($2,''),full_name) END,
		country=CASE WHEN 'country'=ANY(manual_fields) THEN country ELSE COALESCE(NULLIF($3,''),country) END,
		artists=CASE WHEN 'artists'=ANY(manual_fields) OR NOT $12 THEN artists ELSE $4 END,
        genius_id=COALESCE($5,genius_id),music_brainz_id=COALESCE(NULLIF($6,''),music_brainz_id),
        yt_music_id=COALESCE($9,yt_music_id),
        yt_plays=CASE WHEN 'yt_plays'=ANY(manual_fields) THEN yt_plays ELSE COALESCE($10,yt_plays) END,
        yt_subs=CASE WHEN 'yt_subs'=ANY(manual_fields) THEN yt_subs ELSE COALESCE($11,yt_subs) END,
        members_known=members_known OR $12,logo_download_id=CASE
			WHEN EXISTS(SELECT 1 FROM downloads WHERE id=artists.logo_download_id AND (source_type='upload' OR
                (status='finished' AND $7::uuid IS NOT NULL
                    AND NOT EXISTS(SELECT 1 FROM downloads WHERE id=$7 AND status='finished'))))
			THEN logo_download_id ELSE COALESCE($7,logo_download_id) END,
		status='parsed',error=$8,lease_until=NULL,updated_at=now() WHERE id=$1`,
		[]any{
			a.ID, a.FullName, a.Country, a.Artists, a.GeniusID, a.MusicBrainzID, a.LogoDownloadID,
			a.Error, a.YTMusicID, a.YtPlays, a.YtSubs, a.MembersKnown,
		})
}

// SetLogo назначает изображение, загруженное вручную.
func (r ArtistRepository) SetLogo(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, `UPDATE artists SET logo_download_id=$2,updated_at=now(),
        status=CASE WHEN status='manual' THEN 'parsed' ELSE status END WHERE id=$1`, []any{id, downloadID})
}

// Fail сохраняет диагностическую ошибку импорта исполнителя.
func (r ArtistRepository) Fail(ctx context.Context, id uuid.UUID, message string) error {
	return execute(ctx, r.db, `UPDATE artists SET status='errored',error=$2,
		lease_until=NULL,updated_at=now() WHERE id=$1`, []any{id, message})
}

// AlbumRepository управляет альбомами.
type AlbumRepository struct{ db DB }

// List возвращает страницу альбомов с фильтром по исполнителю и статусу.
func (r AlbumRepository) List(ctx context.Context, p domain.Page) ([]domain.Album, error) {
	return query[domain.Album](ctx, r.db, `SELECT * FROM catalog_albums
        WHERE ($3::uuid IS NULL OR artist_id=$3) AND ($4='' OR status=$4)
        ORDER BY `+catalogOrder(p, "created_at,id")+` LIMIT $1 OFFSET $2`,
		[]any{p.Limit, p.Offset, p.ArtistID, p.Status})
}

// Get возвращает альбом по ID.
func (r AlbumRepository) Get(ctx context.Context, id uuid.UUID) (domain.Album, error) {
	return one[domain.Album](ctx, r.db, `SELECT * FROM catalog_albums WHERE id=$1`, []any{id})
}

// Create сохраняет альбом и его владельца одним запросом.
func (r AlbumRepository) Create(ctx context.Context, a domain.Album) (domain.Album, error) {
	err := execute(ctx, r.db, `WITH saved AS (
        INSERT INTO albums(id,name,genius_id,release_date,track_list,track_list_complete,cover_download_id,
            tags,manual_release_date,manual_fields,yt_plays,source_yt_plays,yt_music_id,
            music_brainz_id,excluded_from_quiz)
        VALUES($1,$2,$4,$5,$6,$7,$8,COALESCE($9::text[],'{}'),$10,COALESCE($11::text[],'{}'),$12,$13,$14,$15,$16)
        RETURNING id
    ) INSERT INTO artist_to_album(album_id,artist_id) SELECT id,$3 FROM saved`,
		[]any{
			a.ID, a.Name, a.ArtistID, a.GeniusID, a.ReleaseDate, a.TrackList, a.TrackListComplete, a.CoverDownloadID,
			a.Tags, a.ManualReleaseDate, a.ManualFields, a.YtPlays, a.SourceYtPlays, a.YTMusicID,
			a.MusicBrainzID, a.ExcludedFromQuiz,
		})
	if err != nil {
		return a, err
	}

	return r.Get(ctx, a.ID)
}

// Update сохраняет пользовательские атрибуты альбома.
func (r AlbumRepository) Update(ctx context.Context, a domain.Album) (domain.Album, error) {
	err := execute(ctx, r.db, `UPDATE albums SET name=$2,release_date=$3,manual_release_date=$4,tags=$5,
        excluded_from_quiz=$6,yt_plays=$7,manual_fields=$8,track_list=$9,track_list_complete=$10,
        updated_at=now() WHERE id=$1`,
		[]any{
			a.ID, a.Name, a.ReleaseDate, a.ManualReleaseDate, a.Tags, a.ExcludedFromQuiz, a.YtPlays, a.ManualFields,
			a.TrackList, a.TrackListComplete,
		})
	if err != nil {
		return a, err
	}

	return r.Get(ctx, a.ID)
}

// SetCover назначает обложку, загруженную вручную.
func (r AlbumRepository) SetCover(ctx context.Context, id, downloadID uuid.UUID) error {
	return execute(ctx, r.db, "UPDATE albums SET cover_download_id=$2,updated_at=now() WHERE id=$1", []any{id, downloadID})
}

// Import находит альбом по ID источника или однозначному имени в каталоге исполнителя.
func (r AlbumRepository) Import(ctx context.Context, a domain.Album) (domain.Album, error) {
	matches, err := query[domain.Album](ctx, r.db, `SELECT * FROM catalog_albums WHERE artist_id=$1 AND
        (yt_music_id=$2 OR genius_id=$3 OR ($4<>'' AND music_brainz_id=$4) OR (lower(trim(name))=lower(trim($5))
            AND (yt_music_id IS NULL OR $2::text IS NULL OR yt_music_id=$2)
            AND (genius_id IS NULL OR $3::bigint IS NULL OR genius_id=$3)
            AND (music_brainz_id='' OR $4='' OR music_brainz_id=$4)))
        ORDER BY CASE WHEN yt_music_id=$2 OR genius_id=$3 OR ($4<>'' AND music_brainz_id=$4) THEN 0 ELSE 1 END`,
		[]any{a.ArtistID, a.YTMusicID, a.GeniusID, a.MusicBrainzID, a.Name})
	if err != nil {
		return a, err
	}

	id, err := importMatch(matches, func(m domain.Album) (uuid.UUID, bool) {
		return m.ID, sameID(m.YTMusicID, a.YTMusicID) || sameID(m.GeniusID, a.GeniusID) ||
			(m.MusicBrainzID != "" && m.MusicBrainzID == a.MusicBrainzID)
	})
	if err != nil {
		return a, fmt.Errorf("альбом %q: %w", a.Name, err)
	}

	if id == uuid.Nil {
		return r.Create(ctx, a)
	}

	err = execute(ctx, r.db, `UPDATE albums SET
        release_date=CASE WHEN manual_release_date THEN release_date ELSE COALESCE(NULLIF($2,''),release_date) END,
        track_list=CASE WHEN 'track_list'=ANY(manual_fields) OR (track_list_complete AND NOT $4)
            THEN track_list ELSE COALESCE(NULLIF($3,''),track_list) END,
        track_list_complete=track_list_complete OR $4,
        cover_download_id=CASE WHEN EXISTS(SELECT 1 FROM downloads
            WHERE id=albums.cover_download_id AND (source_type='upload' OR
                (status='finished' AND $5::uuid IS NOT NULL
                    AND NOT EXISTS(SELECT 1 FROM downloads WHERE id=$5 AND status='finished'))))
            THEN cover_download_id ELSE COALESCE($5,cover_download_id) END,
        source_yt_plays=COALESCE($6,source_yt_plays),yt_music_id=COALESCE($7,yt_music_id),
        genius_id=COALESCE($8,genius_id),music_brainz_id=COALESCE(NULLIF($9,''),music_brainz_id),
        updated_at=now() WHERE id=$1`, []any{
		id, a.ReleaseDate, a.TrackList, a.TrackListComplete, a.CoverDownloadID,
		a.SourceYtPlays, a.YTMusicID, a.GeniusID, a.MusicBrainzID,
	})
	if err != nil {
		return a, err
	}

	return r.Get(ctx, id)
}

// Delete удаляет альбом, сохраняя треки без привязки к нему.
func (r AlbumRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM albums WHERE id=$1", []any{id})
}

// TrackRepository управляет треками и их связями.
type TrackRepository struct{ db DB }

const trackColumns = `id,name,artist_id,album_id,tags,yt_plays,asked,guessed,excluded_from_quiz,
    yt_music_id,music_brainz_id,genius_id,manual_fields,missing_fields,status,duration_seconds,created_at,updated_at`

// List возвращает страницу треков со статусами каталога и аудио.
func (r TrackRepository) List(ctx context.Context, p domain.Page) ([]domain.TrackWithStatus, error) {
	return query[domain.TrackWithStatus](ctx, r.db, `SELECT * FROM catalog_tracks
        WHERE ($3::uuid IS NULL OR artist_id=$3) AND ($4::uuid IS NULL OR album_id=$4)
        AND ($5='' OR status=$5) AND ($6='' OR audio_status=$6)
        ORDER BY `+catalogOrder(p, "created_at DESC,id DESC")+` LIMIT $1 OFFSET $2`,
		[]any{p.Limit, p.Offset, p.ArtistID, p.AlbumID, p.Status, p.AudioStatus})
}

// GetWithStatus возвращает трек и состояние его аудиозаписей.
func (r TrackRepository) GetWithStatus(ctx context.Context, id uuid.UUID) (domain.TrackWithStatus, error) {
	return one[domain.TrackWithStatus](ctx, r.db, `SELECT * FROM catalog_tracks WHERE id=$1`, []any{id})
}

// Touch отмечает изменение аудиозаписи трека.
func (r TrackRepository) Touch(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "UPDATE tracks SET updated_at=now() WHERE id=$1", []any{id})
}

// Get возвращает трек по ID.
func (r TrackRepository) Get(ctx context.Context, id uuid.UUID) (domain.Track, error) {
	return one[domain.Track](ctx, r.db, `SELECT `+trackColumns+` FROM catalog_tracks WHERE id=$1`, []any{id})
}

// Create сохраняет трек и проверяет принадлежность альбома исполнителю.
func (r TrackRepository) Create(ctx context.Context, t domain.Track) (domain.Track, error) {
	err := execute(ctx, r.db, `WITH saved AS (
        INSERT INTO tracks(id,name,album_id,tags,genius_id,yt_music_id,music_brainz_id,yt_plays,
            duration_seconds,manual_fields,excluded_from_quiz)
        SELECT $1,$2,$4,COALESCE($5::text[],'{}'),$6,$7,$8,$9,$10,COALESCE($11::text[],'{}'),$12
        WHERE $4::uuid IS NULL OR EXISTS(SELECT 1 FROM artist_to_album WHERE album_id=$4 AND artist_id=$3)
        RETURNING id
    ) INSERT INTO artist_to_track(track_id,artist_id) SELECT id,$3 FROM saved`, []any{
		t.ID, t.Name, t.ArtistID, t.AlbumID, t.Tags, t.GeniusID, t.YTMusicID, t.MusicBrainzID, t.YtPlays,
		t.DurationSeconds, t.ManualFields, t.ExcludedFromQuiz,
	})
	if err != nil {
		return t, err
	}

	return r.Get(ctx, t.ID)
}

// Update сохраняет ручные поля трека.
func (r TrackRepository) Update(ctx context.Context, t domain.Track) (domain.Track, error) {
	err := execute(ctx, r.db, `UPDATE tracks SET name=$2,tags=$3,excluded_from_quiz=$4,yt_plays=$5,
        duration_seconds=$6,manual_fields=$7,album_id=$8,updated_at=now()
        WHERE id=$1 AND (album_id IS NOT DISTINCT FROM $8 OR $8 IS NULL OR EXISTS(
            SELECT 1 FROM artist_to_album WHERE album_id=$8 AND artist_id=$9))`, []any{
		t.ID, t.Name, t.Tags, t.ExcludedFromQuiz, t.YtPlays, t.DurationSeconds, t.ManualFields, t.AlbumID, t.ArtistID,
	})
	if err != nil {
		return t, err
	}

	return r.Get(ctx, t.ID)
}

// Import сопоставляет трек по ID источника, названию, альбому и длительности.
func (r TrackRepository) Import(ctx context.Context, t domain.Track, fallbackPlays *int64) (domain.Track, error) {
	matches, err := query[domain.Track](ctx, r.db, `SELECT `+trackColumns+` FROM catalog_tracks WHERE artist_id=$1 AND
        (yt_music_id=$2 OR genius_id=$3 OR ($4<>'' AND music_brainz_id=$4)
        OR (lower(trim(name))=lower(trim($5)) AND ($6::uuid IS NULL OR album_id IS NULL OR album_id=$6)
            AND ($7::integer IS NULL OR duration_seconds IS NULL OR abs(duration_seconds::bigint-$7)<=1)
            AND (yt_music_id IS NULL OR $2::text IS NULL OR yt_music_id=$2)
            AND (genius_id IS NULL OR $3::bigint IS NULL OR genius_id=$3)
            AND (music_brainz_id='' OR $4='' OR music_brainz_id=$4)))`,
		[]any{t.ArtistID, t.YTMusicID, t.GeniusID, t.MusicBrainzID, t.Name, t.AlbumID, t.DurationSeconds})
	if err != nil {
		return t, err
	}

	id, err := importMatch(matches, func(m domain.Track) (uuid.UUID, bool) {
		return m.ID, sameID(m.YTMusicID, t.YTMusicID) || sameID(m.GeniusID, t.GeniusID) ||
			(m.MusicBrainzID != "" && m.MusicBrainzID == t.MusicBrainzID)
	})
	if err != nil {
		return t, fmt.Errorf("трек %q: %w", t.Name, err)
	}

	if id == uuid.Nil {
		if t.YtPlays == nil {
			t.YtPlays = fallbackPlays
		}

		return r.Create(ctx, t)
	}

	err = execute(ctx, r.db, `UPDATE tracks SET
        album_id=CASE WHEN 'album_id'=ANY(manual_fields) THEN album_id ELSE COALESCE($2,album_id) END,
        yt_plays=CASE WHEN 'yt_plays'=ANY(manual_fields) THEN yt_plays ELSE COALESCE($3,yt_plays,$8) END,
        duration_seconds=CASE WHEN 'duration_seconds'=ANY(manual_fields) THEN duration_seconds
            ELSE COALESCE($4,duration_seconds) END,
        yt_music_id=COALESCE($5,yt_music_id),genius_id=COALESCE($6,genius_id),
        music_brainz_id=COALESCE(NULLIF($7,''),music_brainz_id),updated_at=now() WHERE id=$1`,
		[]any{id, t.AlbumID, t.YtPlays, t.DurationSeconds, t.YTMusicID, t.GeniusID, t.MusicBrainzID, fallbackPlays})
	if err != nil {
		return t, err
	}

	return r.Get(ctx, id)
}

// Delete удаляет трек, если он не используется сохранённым пакетом.
func (r TrackRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM tracks WHERE id=$1", []any{id})
}

func sameID[T comparable](a, b *T) bool {
	return a != nil && b != nil && *a == *b
}

func importMatch[T any](matches []T, identify func(T) (uuid.UUID, bool)) (uuid.UUID, error) {
	var exact uuid.UUID

	for _, m := range matches {
		id, same := identify(m)
		if !same {
			continue
		}

		if exact != uuid.Nil {
			return uuid.Nil, fmt.Errorf("%w: идентификаторы указывают на разные записи", domain.ErrConflict)
		}

		exact = id
	}

	if exact != uuid.Nil || len(matches) == 0 {
		return exact, nil
	}

	if len(matches) > 1 {
		return uuid.Nil, fmt.Errorf("%w: неоднозначное совпадение в каталоге", domain.ErrConflict)
	}

	id, _ := identify(matches[0])

	return id, nil
}
