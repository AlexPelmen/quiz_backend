package store

import (
	"context"

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
	var s domain.Settings

	err := r.db.QueryRow(ctx, "SELECT value FROM settings WHERE id=true").Scan(&s)

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
	return one[domain.ParseRun](ctx, r.db, "INSERT INTO parse_runs(id) VALUES($1) RETURNING *",
		[]any{uuid.New()})
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
	return one[domain.ParseRun](ctx, r.db, `UPDATE parse_runs SET status='running',
		lease_until=now()+make_interval(secs=>$1),updated_at=now() WHERE id=(
		SELECT id FROM parse_runs WHERE status='pending' OR (status='running' AND lease_until<now())
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, []any{seconds})
}

// Renew продлевает захват между исполнителями и обновляет счётчики.
func (r RunRepository) Renew(ctx context.Context, run domain.ParseRun, seconds int) error {
	return execute(ctx, r.db, `UPDATE parse_runs SET processed=$2,failed=$3,
		lease_until=now()+make_interval(secs=>$4),updated_at=now() WHERE id=$1`,
		[]any{run.ID, run.Processed, run.Failed, seconds})
}

// Finish завершает запуск и освобождает его захват.
func (r RunRepository) Finish(ctx context.Context, run domain.ParseRun) error {
	return execute(ctx, r.db, `UPDATE parse_runs SET status=$2,processed=$3,failed=$4,
		error=$5,lease_until=NULL,updated_at=now() WHERE id=$1`,
		[]any{run.ID, run.Status, run.Processed, run.Failed, run.Error})
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
		SELECT id FROM downloads WHERE (status IN ('created','pending') AND next_attempt_at<=now())
		OR (status='processing' AND lease_until<now())
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING *`, []any{seconds})
}

// Finish сохраняет результат либо следующую попытку загрузки.
func (r DownloadRepository) Finish(ctx context.Context, d domain.Download) error {
	return execute(ctx, r.db, `UPDATE downloads SET status=$2,uri=$3,meta=$4,error=$5,
		lease_until=NULL,next_attempt_at=now()+make_interval(secs=>$6),file_type=$8,updated_at=now()
		WHERE id=$1 AND attempts=$7 AND status='processing'`,
		[]any{d.ID, d.Status, d.URI, d.Meta, d.Error, d.Attempts * d.Attempts * 5, d.Attempts, d.FileType})
}

// Retry повторяет ошибочную задачу либо назначает источник ручной задаче.
func (r DownloadRepository) Retry(ctx context.Context, d domain.Download) (domain.Download, error) {
	return one[domain.Download](ctx, r.db, `UPDATE downloads SET source_type=$2,source_uri=$3,
		status='pending',attempts=0,error='',next_attempt_at=now(),updated_at=now()
		WHERE id=$1 AND status IN ('errored','manual') RETURNING *`, []any{d.ID, d.SourceType, d.SourceURI})
}
