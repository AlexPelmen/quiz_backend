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
		_, err := pool.Exec(ctx, `INSERT INTO artists(id, name, tags, created_at)
			VALUES ($1, $2, ARRAY['rock'], $3)`,
			id, fmt.Sprintf("Исполнитель %d", i), time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
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

	if _, err := uow.Artists.Create(ctx, domain.Artist{ID: artistID, Name: "Исполнитель", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}

	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range ids {
		_, err := pool.Exec(ctx, `WITH saved AS (INSERT INTO tracks(id,name,created_at) VALUES ($1,$2,$4) RETURNING id)
            INSERT INTO artist_to_track(track_id,artist_id) SELECT id,$3 FROM saved`,
			id, fmt.Sprintf("Трек %d", i), artistID, time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
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

func TestTrackImportReusesRecordingAcrossArtists(t *testing.T) {
	_, uow := testutil.Database(t)
	ctx := t.Context()
	artists := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range artists {
		if _, err := uow.Artists.Create(ctx, domain.Artist{ID: id, Name: id.String()}); err != nil {
			t.Fatal(err)
		}
	}

	videoID := "MyS3DIFaXF4"
	recording := uuid.New()
	for i, artistID := range []uuid.UUID{artists[0], artists[1], artists[1]} {
		id := recording
		if i > 0 {
			id = uuid.New()
		}
		if err := uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
			saved, previous, err := tx.Tracks.Fill(ctx, domain.Track{
				ID: id, ArtistID: artistID, Name: "RATATATA", YTMusicID: &videoID,
			})
			if err != nil {
				return err
			}
			if saved.ID != recording || saved.ArtistID != artistID || (previous.ID == uuid.Nil) != (i == 0) {
				return fmt.Errorf("неверный результат импорта: %+v, предыдущий ID %s", saved, previous.ID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, artistID := range artists {
		tracks, err := uow.Tracks.List(ctx, domain.Page{ArtistID: &artistID, Limit: 10})
		if err != nil || len(tracks) != 1 || tracks[0].ID != recording || tracks[0].ArtistID != artistID {
			t.Fatalf("каталог исполнителя %s: %+v, %v", artistID, tracks, err)
		}
	}

	tracks, err := uow.Tracks.List(ctx, domain.Page{Limit: 10})
	if err != nil || len(tracks) != 1 || tracks[0].ArtistID != artists[0] {
		t.Fatalf("общий каталог: %+v, %v", tracks, err)
	}
	if err := uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		return tx.Artists.Delete(ctx, artists[0])
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := uow.Tracks.Get(ctx, recording)
	if err != nil || saved.ArtistID != artists[1] {
		t.Fatalf("совместная запись после удаления основного исполнителя: %+v, %v", saved, err)
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
		downloads, err := uow.Downloads.List(ctx, domain.Page{Limit: 1, Offset: page, Status: "created"})
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

	sql := `DELETE FROM schema_migrations
		WHERE version IN ('002_source_retry_settings.sql','016_short_source_retries.sql')`
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

	if s.Source429Retries != 7 || s.Source429DelaySeconds != 1 ||
		s.Source429MaxDelaySeconds != 3 || s.GeniusRequestIntervalMS != 1000 {
		t.Fatalf("миграция потеряла настройки: %+v", s)
	}
}

func TestTransactionsAndQueue(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	artistID := uuid.New()
	wantErr := errors.New("откат")
	err := uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		_, err := tx.Artists.Create(ctx, domain.Artist{ID: artistID, Name: "Откат", Tags: []string{}})
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

	if _, err := uow.Downloads.Claim(ctx, 60); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("не запущенная задача захвачена: %v", err)
	}

	if _, err := uow.Downloads.Retry(ctx, d); err != nil {
		t.Fatal(err)
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
		c, err := uow.Packs.Pick(ctx, []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track", Center: 0.2})
		if err != nil {
			t.Fatal(err)
		}

		counts[c.Artist.ID]++
	}

	if counts[high] < 150 {
		t.Fatalf("выбор не учитывает веса: %v", counts)
	}

	selected, err := uow.Packs.Pick(ctx, []uuid.UUID{high}, store.PickFilter{Kind: "artist_by_track", Center: 0.2})
	if err != nil || selected.Artist.ID != low {
		t.Fatalf("не исключён исполнитель: %v", err)
	}

	for _, table := range []string{"artists", "tracks"} {
		t.Run(table, func(t *testing.T) { assertZeroExcludes(t, pool, uow, table) })
	}
}

func TestGuessRateThreshold(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	high := seedCandidate(t, pool, 100)

	low := seedCandidate(t, pool, 1)
	if _, err := pool.Exec(ctx, "UPDATE artists SET asked=6,guessed=3 WHERE id=$1", low); err != nil {
		t.Fatal(err)
	}

	for _, scenario := range []struct {
		name    string
		asked   int
		enabled bool
		want    uuid.UUID
	}{
		{"at threshold", 5, true, high},
		{"above threshold", 6, true, low},
		{"disabled", 6, false, high},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "UPDATE artists SET asked=$2 WHERE id=$1", high, scenario.asked); err != nil {
				t.Fatal(err)
			}

			filter := store.PickFilter{
				Kind: "artist_by_track", Center: 0.2, GuessRateEnabled: scenario.enabled, AskedThreshold: 5,
			}
			count := 0

			for range 30 {
				pick, err := uow.Packs.Pick(ctx, []uuid.UUID{}, filter)
				if err != nil {
					t.Fatal(err)
				}

				if pick.Artist.ID == scenario.want {
					count++
				}
			}

			if count < 25 {
				t.Fatalf("ожидаемый исполнитель выбран %d из 30 раз", count)
			}
		})
	}
}

func seedCandidate(t *testing.T, pool *pgxpool.Pool, plays int) uuid.UUID {
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
		{"INSERT INTO artists(id,name,status,yt_plays) VALUES($1,$2,'parsed',$3)", []any{a, a.String(), plays}},
		{
			`WITH saved AS (INSERT INTO albums(id,name) VALUES($1,'Альбом') RETURNING id)
                INSERT INTO artist_to_album(album_id,artist_id) SELECT id,$2 FROM saved`,
			[]any{al, a},
		},
		{
			`WITH saved AS (INSERT INTO tracks(id,name,album_id) VALUES($1,'Трек',$3) RETURNING id)
                INSERT INTO artist_to_track(track_id,artist_id) SELECT id,$2 FROM saved`,
			[]any{tr, a, al},
		},
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
		if _, err := pool.Exec(t.Context(), fmt.Sprintf("UPDATE %s SET excluded_from_quiz=true", table)); err != nil {
			return err
		}

		_, err := tx.Packs.Pick(t.Context(), []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track", Center: 0.2})
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("нуль в %s не исключает данные: %w", table, err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reset := fmt.Sprintf("UPDATE %s SET excluded_from_quiz=false", table)
	if _, err := pool.Exec(context.Background(), reset); err != nil {
		t.Fatal(err)
	}
}
