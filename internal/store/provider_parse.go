package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// ClaimProvider забирает исполнителя и обрабатывает его не чаще раза за прогон.
func (r ArtistRepository) ClaimProvider(ctx context.Context, seconds int, startedAt time.Time, provider string) (domain.Artist, error) {
	id, err := one[struct{ ID uuid.UUID }](ctx, r.db, `UPDATE artists SET status='processing',
        provider_parsed=true,lease_until=now()+make_interval(secs=>$1),updated_at=now() WHERE id=(
        SELECT a.id FROM artists a JOIN catalog_artists c ON c.id=a.id
        CROSS JOIN LATERAL (SELECT 1+count(*) AS n FROM unnest($4::bigint[]) AS threshold
            WHERE threshold<=a.yt_subs) AS album_limit
        WHERE (a.status='processing' AND a.lease_until<now()) OR (a.updated_at <= $2 AND (
            a.status IN ('created','error','errored') OR
            (a.status IN ('partial','parsed') AND (c.status IN ('partial','manual') OR EXISTS (
                SELECT 1 FROM catalog_albums al WHERE al.artist_id=a.id AND cardinality(al.missing_fields)>0
            ) OR EXISTS (
                SELECT 1 FROM catalog_tracks t WHERE t.artist_id=a.id AND cardinality(t.missing_fields)>0
            ) OR (
                (SELECT count(*) FROM artist_to_album WHERE artist_id=a.id)<album_limit.n
                AND NOT EXISTS (SELECT 1 FROM artist_provider_lists
                    WHERE artist_id=a.id AND provider=$3 AND kind='albums' AND artist_provider_lists.album_limit>=album_limit.n)
            )))
        ))
        ORDER BY CASE WHEN a.status='created' THEN 1 ELSE 0 END,a.created_at,a.id
        FOR UPDATE OF a SKIP LOCKED LIMIT 1) RETURNING id`, []any{seconds, startedAt, provider, domain.AlbumSubscriberThresholds()})
	if err != nil {
		return domain.Artist{}, err
	}
	return r.Get(ctx, id.ID)
}

// FillProvider дополняет пустые поля исполнителя, удерживая захват очереди.
func (r ArtistRepository) FillProvider(ctx context.Context, a domain.Artist) error {
	return execute(ctx, r.db, `UPDATE artists SET
        full_name=COALESCE(NULLIF(full_name,''),$2),country=COALESCE(NULLIF(country,''),$3),
        artists=CASE WHEN NOT members_known AND $4 THEN $5 ELSE artists END,
        members_known=members_known OR $4,
        yt_subs=COALESCE(yt_subs,$6),yt_plays=COALESCE(yt_plays,$7),
        yt_music_id=COALESCE(yt_music_id,$8),genius_id=COALESCE(genius_id,$9),
        music_brainz_id=COALESCE(NULLIF(music_brainz_id,''),$10),
        logo_download_id=CASE WHEN logo_download_id IS NULL OR EXISTS (
            SELECT 1 FROM downloads WHERE id=logo_download_id AND status IN ('manual','errored'))
            THEN COALESCE($11,logo_download_id) ELSE logo_download_id END
        WHERE id=$1 AND status='processing'`, []any{
		a.ID, a.FullName, a.Country, a.MembersKnown, a.Artists, a.YtSubs, a.YtPlays, a.YTMusicID, a.GeniusID, a.MusicBrainzID, a.LogoDownloadID,
	})
}

// FinishProvider освобождает захват после сохранения результатов провайдера.
func (r ArtistRepository) FinishProvider(ctx context.Context, id uuid.UUID, message string) error {
	return execute(ctx, r.db, `UPDATE artists SET status=CASE WHEN $2<>'' THEN 'error'
        WHEN EXISTS (SELECT 1 FROM catalog_artists WHERE id=$1 AND cardinality(missing_fields)>0)
        THEN 'partial' ELSE 'parsed' END,
        error=$2,lease_until=NULL,updated_at=now() WHERE id=$1 AND status='processing'`, []any{id, message})
}

// ProviderLists читает списки, которые провайдер уже вернул без ошибки.
func (r ArtistRepository) ProviderLists(ctx context.Context, id uuid.UUID, provider string, albumLimit int) ([]string, error) {
	rows, err := query[struct{ Kind string }](ctx, r.db, `SELECT kind FROM artist_provider_lists
        WHERE artist_id=$1 AND provider=$2 AND (kind<>'albums' OR album_limit >= $3)`, []any{id, provider, albumLimit})
	kinds := make([]string, 0, len(rows))
	for _, row := range rows {
		kinds = append(kinds, row.Kind)
	}
	return kinds, err
}

// FinishProviderList фиксирует завершённую выборку источника, включая пустой результат.
func (r ArtistRepository) FinishProviderList(ctx context.Context, id uuid.UUID, provider, kind string, albumLimit int) error {
	_, err := r.db.Exec(ctx, `INSERT INTO artist_provider_lists(artist_id,provider,kind,album_limit)
        VALUES($1,$2,$3,$4) ON CONFLICT (artist_id,provider,kind) DO UPDATE
        SET album_limit=GREATEST(artist_provider_lists.album_limit,excluded.album_limit)`, id, provider, kind, albumLimit)
	return translate(err)
}

// SaveProviderCounts обновляет счётчики текущего прогона.
func (r RunRepository) SaveProviderCounts(ctx context.Context, run domain.ParseRun) error {
	return execute(ctx, r.db, `UPDATE parse_runs SET fields=$2,albums=$3,tracks=$4,errors=$5,
        processed=$6,failed=$7,updated_at=now() WHERE id=$1`,
		[]any{run.ID, run.Fields, run.Albums, run.Tracks, run.Errors, run.Processed, run.Failed})
}

// SetProviderError сохраняет или снимает отметку сбоя сценария для альбома или трека.
func (r RunRepository) SetProviderError(ctx context.Context, id uuid.UUID, provider, scenario string, failed bool) error {
	if failed {
		_, err := r.db.Exec(ctx, `WITH album_error AS (
            INSERT INTO album_parse_errors(entity_id,provider,scenario)
            SELECT id,$2,$3 FROM albums WHERE id=$1 ON CONFLICT DO NOTHING
        ) INSERT INTO track_parse_errors(entity_id,provider,scenario)
            SELECT id,$2,$3 FROM tracks WHERE id=$1 ON CONFLICT DO NOTHING`, id, provider, scenario)
		return translate(err)
	}
	_, err := r.db.Exec(ctx, `WITH album_error AS (
        DELETE FROM album_parse_errors WHERE entity_id=$1 AND provider=$2 AND scenario=$3
    ) DELETE FROM track_parse_errors WHERE entity_id=$1 AND provider=$2 AND scenario=$3`, id, provider, scenario)
	return translate(err)
}
