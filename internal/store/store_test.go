package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestQuotaKeepsNewestObservation(t *testing.T) {
	_, uow := testutil.Database(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)

	first := domain.GeniusQuota{Limit: 10000, Remaining: 99, ObservedAt: now, ResetAt: now.Add(time.Hour)}
	if err := uow.Quota.Save(ctx, first); err != nil {
		t.Fatal(err)
	}

	older := first
	older.ObservedAt = now.Add(-time.Second)
	older.Remaining = 100

	if err := uow.Quota.Save(ctx, older); err != nil {
		t.Fatal(err)
	}

	got, err := uow.Quota.Get(ctx)
	if err != nil || got.Remaining != 99 {
		t.Fatalf("старое наблюдение перезаписало новое: %+v %v", got, err)
	}
}

func TestArtistsListNewestFirstAcrossPages(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range ids {
		_, err := pool.Exec(ctx, `INSERT INTO artists(id, name, tags, points, created_at)
			VALUES ($1, $2, ARRAY['rock'], 100, $3)`, id, fmt.Sprintf("Исполнитель %d", i), time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
	}
	for page := range ids {
		artists, err := uow.Artists.List(ctx, domain.Page{Limit: 1, Offset: page, Tag: "rock"})
		if err != nil || len(artists) != 1 || artists[0].ID != ids[len(ids)-1-page] {
			t.Fatalf("неверный порядок страницы %d: %+v, ошибка: %v", page, artists, err)
		}
	}
}

func TestTracksListNewestFirstAcrossPages(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	artistID := uuid.New()
	if _, err := uow.Artists.Create(ctx, domain.Artist{ID: artistID, Name: "Исполнитель", Tags: []string{}, Points: 100}); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range ids {
		_, err := pool.Exec(ctx, `INSERT INTO tracks(id, name, artist_id, created_at)
			VALUES ($1, $2, $3, $4)`, id, fmt.Sprintf("Трек %d", i), artistID, time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
	}
	for page := range ids {
		tracks, err := uow.Tracks.List(ctx, domain.Page{Limit: 1, Offset: page, ArtistID: &artistID})
		if err != nil || len(tracks) != 1 || tracks[0].ID != ids[len(ids)-1-page] {
			t.Fatalf("неверный порядок страницы %d: %+v, ошибка: %v", page, tracks, err)
		}
	}
}

func TestDownloadsListNewestFirstAcrossPages(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range ids {
		_, err := pool.Exec(ctx, `INSERT INTO downloads(id, source_type, file_type, created_at)
			VALUES ($1, 'mp3', 'mp3', $2)`, id, time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
	}
	for page := range ids {
		downloads, err := uow.Downloads.List(ctx, domain.Page{Limit: 1, Offset: page, Status: "pending"})
		if err != nil || len(downloads) != 1 || downloads[0].ID != ids[len(ids)-1-page] {
			t.Fatalf("неверный порядок страницы %d: %+v, ошибка: %v", page, downloads, err)
		}
	}
}

func TestSourceRetrySettingsMigration(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()

	if _, err := pool.Exec(ctx, `UPDATE settings SET value =
		(value - 'genius_request_interval_ms' - 'source_429_delay_seconds' - 'source_429_max_delay_seconds')
		|| '{"source_429_retries":7}'::jsonb`); err != nil {
		t.Fatal(err)
	}

	sql := "DELETE FROM schema_migrations WHERE version='002_source_retry_settings.sql'"
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := uow.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}

	s, err := uow.Settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}

	if s.Source429Retries != 7 || s.Source429DelaySeconds != 30 || s.GeniusRequestIntervalMS != 1000 {
		t.Fatalf("миграция потеряла настройки: %+v", s)
	}
}

func TestTransactionsAndQueue(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	artistID := uuid.New()
	wantErr := errors.New("откат")
	err := uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		_, err := tx.Artists.Create(ctx, domain.Artist{ID: artistID, Name: "Откат", Tags: []string{}, Points: 100})
		if err != nil {
			return err
		}

		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatal(err)
	}

	if _, err := uow.Artists.Get(ctx, artistID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("не откатился исполнитель: %v", err)
	}

	d, err := uow.Downloads.Create(ctx, domain.Download{
		ID: uuid.New(), SourceType: "mp3",
		SourceURI: "https://example.com/a.mp3", FileType: "mp3", Meta: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	duplicate, err := uow.Downloads.Create(ctx, domain.Download{
		ID: uuid.New(), SourceType: "mp3",
		SourceURI: d.SourceURI, FileType: "mp3", Meta: map[string]any{},
	})
	if err != nil || duplicate.ID != d.ID {
		t.Fatalf("дубликат загрузки: %v", err)
	}

	claimed, err := uow.Downloads.Claim(ctx, 60)
	if err != nil || claimed.ID != d.ID || claimed.Attempts != 1 {
		t.Fatalf("захват: %+v %v", claimed, err)
	}

	if _, err := uow.Downloads.Claim(ctx, 60); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("задача захвачена повторно: %v", err)
	}

	sql := "UPDATE downloads SET lease_until=now()-interval '1 second' WHERE id=$1"
	if _, err := pool.Exec(ctx, sql, d.ID); err != nil {
		t.Fatal(err)
	}

	recovered, err := uow.Downloads.Claim(ctx, 60)
	if err != nil || recovered.ID != d.ID || recovered.Attempts != 2 {
		t.Fatalf("восстановление: %+v %v", recovered, err)
	}
}

func TestWeightedSelection(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	low := seedCandidate(t, pool, 1)
	high := seedCandidate(t, pool, 100)
	counts := map[uuid.UUID]int{}

	for range 200 {
		c, err := uow.Packs.Pick(ctx, []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track"})
		if err != nil {
			t.Fatal(err)
		}

		counts[c.Artist.ID]++
	}

	if counts[high] < 150 {
		t.Fatalf("выбор не учитывает веса: %v", counts)
	}

	selected, err := uow.Packs.Pick(ctx, []uuid.UUID{high}, store.PickFilter{Kind: "artist_by_track"})
	if err != nil || selected.Artist.ID != low {
		t.Fatalf("не исключён исполнитель: %v", err)
	}

	for _, table := range []string{"artists", "tracks"} {
		t.Run(table, func(t *testing.T) { assertZeroExcludes(t, pool, uow, table) })
	}
}

func seedCandidate(t *testing.T, pool *pgxpool.Pool, points int) uuid.UUID {
	t.Helper()

	a, al, tr, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ctx := t.Context()
	queries := []struct {
		sql  string
		args []any
	}{
		{
			"INSERT INTO downloads(id,source_type,status,file_type,meta) VALUES($1,'mp3','finished','mp3',$2)",
			[]any{d, map[string]any{"duration_seconds": 60}},
		},
		{"INSERT INTO artists(id,name,status,points) VALUES($1,$2,'parsed',$3)", []any{a, a.String(), points}},
		{"INSERT INTO albums(id,name,artist_id) VALUES($1,'Альбом',$2)", []any{al, a}},
		{"INSERT INTO tracks(id,name,artist_id,album_id) VALUES($1,'Трек',$2,$3)", []any{tr, a, al}},
		{"INSERT INTO track_to_download VALUES($1,$2)", []any{tr, d}},
	}

	for _, q := range queries {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	return a
}

func assertZeroExcludes(t *testing.T, pool *pgxpool.Pool, uow *store.UnitOfWork, table string) {
	t.Helper()

	err := uow.Transaction(t.Context(), func(tx *store.UnitOfWork) error {
		// Отдельный пул используется только для настройки сценария; транзакция выбора видит завершённую запись.
		if _, err := pool.Exec(t.Context(), fmt.Sprintf("UPDATE %s SET points=0", table)); err != nil {
			return err
		}

		_, err := tx.Packs.Pick(t.Context(), []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track"})
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("нуль в %s не исключает данные: %w", table, err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(context.Background(), fmt.Sprintf("UPDATE %s SET points=100", table)); err != nil {
		t.Fatal(err)
	}
}
