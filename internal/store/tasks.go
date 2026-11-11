package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// SettingsRepository хранит типизированную конфигурацию в JSONB.
type SettingsRepository struct{ db DB }

// Seed создаёт настройки только при первом запуске.
func (r SettingsRepository) Seed(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `INSERT INTO settings(id,value) VALUES(true,$1)
		ON CONFLICT DO NOTHING`, domain.DefaultSettings())
	return err
}

// Get читает текущую конфигурацию.
func (r SettingsRepository) Get(ctx context.Context) (domain.Settings, error) {
	s := domain.DefaultSettings()

	var data []byte

	if err := r.db.QueryRow(ctx, "SELECT value FROM settings WHERE id=true").Scan(&data); err != nil {
		return s, err
	}

	err := json.Unmarshal(data, &s)

	return s, err
}

// Save заменяет конфигурацию после проверки сервисом.
func (r SettingsRepository) Save(ctx context.Context, s domain.Settings) error {
	return execute(ctx, r.db, "UPDATE settings SET value=$1 WHERE id=true", []any{s})
}

// RunRepository хранит состояние явно запущенного парсинга.
type RunRepository struct{ db DB }

// Create создаёт запуск; уникальный индекс запрещает одновременные запуски.
func (r RunRepository) Create(ctx context.Context) (domain.ParseRun, error) {
	return r.CreateProvider(ctx, "")
}

// CreateProvider сохраняет выбранный источник запуска.
func (r RunRepository) CreateProvider(ctx context.Context, provider string) (domain.ParseRun, error) {
	return one[domain.ParseRun](ctx, r.db, "INSERT INTO parse_runs(id,provider) VALUES($1,$2) RETURNING *",
		[]any{uuid.New(), provider})
}

// Get возвращает состояние запуска.
func (r RunRepository) Get(ctx context.Context, id uuid.UUID) (domain.ParseRun, error) {
	return one[domain.ParseRun](ctx, r.db, "SELECT * FROM parse_runs WHERE id=$1", []any{id})
}

// List возвращает запуски от новых к старым.
func (r RunRepository) List(ctx context.Context, p domain.Page) ([]domain.ParseRun, error) {
	return query[domain.ParseRun](ctx, r.db, `SELECT * FROM parse_runs
		ORDER BY created_at DESC,id LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset})
}

// Claim захватывает очередь или восстанавливает просроченный запуск.
func (r RunRepository) Claim(ctx context.Context, seconds int) (domain.ParseRun, error) {
	return one[domain.ParseRun](ctx, r.db, `UPDATE parse_runs SET status=CASE WHEN status='stopping' THEN status ELSE 'running' END,
		lease_until=now()+make_interval(secs=>$1),updated_at=now() WHERE id=(
		SELECT id FROM parse_runs WHERE status='pending' OR (status IN ('running','stopping') AND lease_until<now())
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, []any{seconds})
}

// Renew продлевает захват между исполнителями и обновляет счётчики.
func (r RunRepository) Renew(ctx context.Context, run domain.ParseRun, seconds int) (domain.ParseRun, error) {
	return one[domain.ParseRun](ctx, r.db, `UPDATE parse_runs SET processed=$2,failed=$3,
		lease_until=now()+make_interval(secs=>$4),updated_at=now() WHERE id=$1 RETURNING *`,
		[]any{run.ID, run.Processed, run.Failed, seconds})
}

// Stop запрашивает остановку после текущего исполнителя.
func (r RunRepository) Stop(ctx context.Context, id uuid.UUID) (domain.ParseRun, error) {
	return one[domain.ParseRun](ctx, r.db, `UPDATE parse_runs SET status=CASE
		WHEN status='pending' THEN 'stopped' ELSE 'stopping' END, updated_at=now()
		WHERE id=$1 AND status IN ('pending','running','stopping') RETURNING *`, []any{id})
}

// Finish завершает запуск и освобождает его захват.
func (r RunRepository) Finish(ctx context.Context, run domain.ParseRun) error {
	return execute(ctx, r.db, `UPDATE parse_runs SET status=CASE WHEN status='stopping' THEN 'stopped' ELSE $2 END,processed=$3,failed=$4,
		error=$5,lease_until=NULL,updated_at=now(),fields=$6,albums=$7,tracks=$8,errors=$9 WHERE id=$1`,
		[]any{run.ID, run.Status, run.Processed, run.Failed, run.Error, run.Fields, run.Albums, run.Tracks, run.Errors})
}

// DownloadRepository управляет очередью скачивания.
type DownloadRepository struct{ db DB }

// ImageIsArtist сообщает, используется ли изображение исполнителем.
func (r DownloadRepository) ImageIsArtist(ctx context.Context, id uuid.UUID) (bool, error) {
	var isArtist bool

	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE logo_download_id=$1)`, id).Scan(&isArtist)

	return isArtist, err
}

// Create обеспечивает повторное использование уже известного источника.
func (r DownloadRepository) Create(ctx context.Context, d domain.Download) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, `INSERT INTO downloads(id,source_type,source_uri,file_type,meta)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(source_type,source_uri) WHERE source_uri<>''
		DO UPDATE SET source_uri=EXCLUDED.source_uri RETURNING *`,
		[]any{d.ID, d.SourceType, d.SourceURI, d.FileType, d.Meta})
}

// SaveUpload регистрирует проверенный локальный файл как завершённую загрузку.
func (r DownloadRepository) SaveUpload(ctx context.Context, d domain.Download) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, `INSERT INTO downloads(id,source_type,uri,status,file_type,meta)
		VALUES($1,'upload',$2,'finished',$3,$4) RETURNING *`, []any{d.ID, d.URI, d.FileType, d.Meta})
}

// Link связывает трек с записью или задачей ручного поиска.
func (r DownloadRepository) Link(ctx context.Context, trackID, downloadID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `INSERT INTO track_to_download VALUES($1,$2) ON CONFLICT DO NOTHING`,
		trackID, downloadID)
	return err
}

// Get возвращает загрузку по ID.
func (r DownloadRepository) Get(ctx context.Context, id uuid.UUID) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, "SELECT * FROM downloads WHERE id=$1", []any{id})
}

// List возвращает страницу загрузок, при необходимости конкретного трека.
func (r DownloadRepository) List(ctx context.Context, p domain.Page) ([]domain.Download, error) {
	return query[domain.Download](ctx, r.db, `SELECT d.* FROM downloads d
		WHERE ($3='' OR d.status=$3) AND ($4::uuid IS NULL OR EXISTS(
		SELECT 1 FROM track_to_download td WHERE td.download_id=d.id AND td.track_id=$4))
		ORDER BY ($4::uuid IS NOT NULL AND d.source_type='upload') DESC,d.created_at DESC,d.id DESC
		LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.Status, p.TrackID})
}

// Claim атомарно захватывает задачу; истёкший захват позволяет восстановить работу.
func (r DownloadRepository) Claim(ctx context.Context, seconds int) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, `UPDATE downloads SET status='processing',attempts=attempts+1,
		lease_until=now()+make_interval(secs=>$1),updated_at=now() WHERE id=(
		SELECT id FROM downloads WHERE (status='pending' AND next_attempt_at<=now())
		OR (status='processing' AND lease_until<now())
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, []any{seconds})
}

// Finish сохраняет результат либо следующую попытку загрузки.
func (r DownloadRepository) Finish(ctx context.Context, d domain.Download) error {
	return execute(ctx, r.db, `UPDATE downloads SET status=$2,uri=$3,meta=$4,error=$5,
		lease_until=NULL,next_attempt_at=now()+make_interval(secs=>$6),file_type=$8,preview=$9,updated_at=now()
		WHERE id=$1 AND attempts=$7 AND status='processing'`,
		[]any{d.ID, d.Status, d.URI, d.Meta, d.Error, d.Attempts * d.Attempts * 5, d.Attempts, d.FileType, d.Preview})
}

// Retry повторяет ошибочную задачу либо назначает источник ручной задаче.
func (r DownloadRepository) Retry(ctx context.Context, d domain.Download) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, `UPDATE downloads SET source_type=$2,source_uri=$3,
		downloader='direct',preview=false,status='pending',attempts=0,error='',next_attempt_at=now(),updated_at=now()
		WHERE id=$1 AND status IN ('created','errored','manual') RETURNING *`, []any{d.ID, d.SourceType, d.SourceURI})
}

// Start ставит выбранные задания в очередь. Вызывается внутри транзакции.
func (r DownloadRepository) Start(ctx context.Context, downloader string, ids []uuid.UUID) (int64, error) {
	// Захватываем строки по ID, чтобы пересекающиеся запуски брали блокировки в одном порядке.
	result, err := r.db.Exec(ctx, `WITH ready AS (
        SELECT id FROM downloads WHERE id=ANY($1::uuid[]) AND status IN ('created','errored','manual')
        AND CASE WHEN $2='image' THEN source_type='image' AND source_uri<>''
            ELSE file_type='mp3' AND EXISTS(SELECT 1 FROM track_to_download WHERE download_id=downloads.id) END
        ORDER BY id FOR UPDATE
    ) UPDATE downloads SET status='pending',downloader=$2,preview=false,attempts=0,error='',
        next_attempt_at=now(),lease_until=NULL,updated_at=now() WHERE id IN (SELECT id FROM ready)`, ids, downloader)
	if err != nil {
		return 0, translate(err)
	}

	if result.RowsAffected() != int64(len(ids)) {
		return 0, domain.ErrConflict
	}

	return result.RowsAffected(), nil
}

// StartAllWaiting ставит в очередь все ожидающие задания выбранного типа.
func (r DownloadRepository) StartAllWaiting(ctx context.Context, downloader string) (int64, error) {
	result, err := r.db.Exec(ctx, `WITH ready AS (
        SELECT id FROM downloads WHERE status='created'
        AND CASE WHEN $1='image' THEN source_type='image' AND source_uri<>''
            ELSE file_type='mp3' AND EXISTS(SELECT 1 FROM track_to_download WHERE download_id=downloads.id) END
        ORDER BY id FOR UPDATE
    ) UPDATE downloads SET status='pending',downloader=$1,preview=false,attempts=0,error='',
        next_attempt_at=now(),lease_until=NULL,updated_at=now() WHERE id IN (SELECT id FROM ready)`, downloader)
	if err != nil {
		return 0, translate(err)
	}
	return result.RowsAffected(), nil
}

// TrackIDs возвращает треки, связанные с задачей.
func (r DownloadRepository) TrackIDs(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := query[struct{ TrackID uuid.UUID }](ctx, r.db,
		"SELECT track_id FROM track_to_download WHERE download_id=$1 ORDER BY track_id", []any{id})
	if err != nil {
		return nil, err
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.TrackID)
	}

	return ids, nil
}

// HasAudioJob проверяет, есть ли готовая запись или существующее задание трека.
func (r DownloadRepository) HasAudioJob(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool

	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM track_to_download td JOIN downloads d ON d.id=td.download_id
        WHERE td.track_id=$1 AND d.file_type='mp3')`, id).Scan(&exists)

	return exists, err
}

// FillTrackDuration восстанавливает длительность, если у трека её ещё нет.
func (r DownloadRepository) FillTrackDuration(ctx context.Context, id uuid.UUID, seconds int) error {
	_, err := r.db.Exec(ctx, `UPDATE tracks SET duration_seconds=$2,updated_at=now()
        WHERE duration_seconds IS NULL
            AND id IN (SELECT track_id FROM track_to_download WHERE download_id=$1)`, id, seconds)

	return translate(err)
}
