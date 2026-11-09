package store_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestCatalogMetricsMigration(t *testing.T) {
	pool := testutil.DatabaseUnmigrated(t)
	ctx := t.Context()

	migrateLegacyCatalog(t, pool)
	seedLegacyCatalog(t, pool)

	uow := store.New(pool)
	for range 2 {
		if err := uow.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}

	artistID := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	artist, err := uow.Artists.Get(ctx, artistID)
	if err != nil || !artist.ExcludedFromQuiz || artist.Asked != 0 || artist.YtPlays != nil {
		t.Fatalf("исполнитель: %+v %v", artist, err)
	}

	albums, err := uow.Albums.List(ctx, domain.Page{Limit: 10, ArtistID: &artistID})
	if err != nil || len(albums) != 1 || albums[0].Asked != 3 || albums[0].Guessed != 2 {
		t.Fatalf("альбомы: %+v %v", albums, err)
	}

	tracks, err := uow.Tracks.List(ctx, domain.Page{Limit: 10, ArtistID: &artistID})
	if err != nil || len(tracks) != 1 || tracks[0].AlbumID == nil || *tracks[0].AlbumID != albums[0].ID ||
		tracks[0].Asked != 0 || tracks[0].AudioStatus != "finished" {
		t.Fatalf("треки: %+v %v", tracks, err)
	}

	var points float64

	var oldCount, answers int
	if err := pool.QueryRow(ctx, `SELECT points,guess_num,(SELECT count(*) FROM quiz_round_results)
        FROM legacy_catalog_popularity WHERE entity='albums' AND id=$1`, albums[0].ID).
		Scan(&points, &oldCount, &answers); err != nil ||
		points != 70.5 || oldCount != 11 || answers != 1 {
		t.Fatalf("история: %v %d %d %v", points, oldCount, answers, err)
	}

	pack, err := uow.Packs.Get(ctx, uuid.MustParse("00000000-0000-0000-0000-000000000006"))
	if err != nil || len(pack.Rounds) != 1 || len(pack.Rounds[0].Questions) != 1 ||
		pack.Rounds[0].Questions[0].SoundDownloadID == nil {
		t.Fatalf("пакет: %+v %v", pack, err)
	}

	if _, err := uow.Downloads.Claim(ctx, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("автозапуск после миграции: %v", err)
	}

	if _, err := pool.Exec(ctx, "INSERT INTO tracks(id,name) VALUES($1,'Без исполнителя')", uuid.New()); err == nil {
		t.Fatal("создан трек без связи с исполнителем")
	}
}

func TestAlbumSourcePlaysPrecedeLinkedTrackEstimate(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()

	artist, err := uow.Artists.Create(ctx, domain.Artist{Name: "Artist"})
	if err != nil {
		t.Fatal(err)
	}

	album, err := uow.Albums.Create(ctx, domain.Album{Name: "Album", ArtistID: artist.ID})
	if err != nil {
		t.Fatal(err)
	}

	plays := int64(1000)
	if _, err := uow.Tracks.Create(ctx, domain.Track{
		Name: "Track", ArtistID: artist.ID, AlbumID: &album.ID, YtPlays: &plays,
	}); err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		update string
		want   int64
	}{
		{"", 1000},
		{"UPDATE albums SET source_yt_plays=200 WHERE id=$1", 200},
		{"UPDATE albums SET yt_plays=50, manual_fields=ARRAY['yt_plays'] WHERE id=$1", 50},
	} {
		if step.update != "" {
			if _, err := pool.Exec(ctx, step.update, album.ID); err != nil {
				t.Fatal(err)
			}
		}

		got, err := uow.Albums.Get(ctx, album.ID)
		if err != nil || got.YtPlays == nil || *got.YtPlays != step.want {
			t.Fatalf("после %q ожидали %d: %+v, %v", step.update, step.want, got, err)
		}
	}
}

func migrateLegacyCatalog(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := t.Context()

	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = pool.Exec(ctx, "CREATE TABLE schema_migrations(version text PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		if strings.Compare(file.Name(), "013_catalog_metrics.sql") >= 0 {
			break
		}

		data, err := os.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}

		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", file.Name(), err)
		}

		if _, err = pool.Exec(ctx, "INSERT INTO schema_migrations VALUES ($1)", file.Name()); err != nil {
			t.Fatal(err)
		}
	}
}

func seedLegacyCatalog(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := t.Context()

	const seed = `
    INSERT INTO downloads(id,source_type,status,file_type,uri) VALUES
        ('00000000-0000-0000-0000-000000000001','mp3','finished','mp3','/test/track.mp3'),
        ('00000000-0000-0000-0000-000000000002','youtube','processing','mp3','');
    INSERT INTO artists(id,name,status,points,guess_num) VALUES
        ('00000000-0000-0000-0000-000000000003','Artist','parsed',0,7);
    INSERT INTO albums(id,artist_id,name,points,guess_num) VALUES
        ('00000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000003','Album',70.5,11);
    INSERT INTO tracks(id,artist_id,album_id,name,points,guess_num) VALUES
        ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000003',
         '00000000-0000-0000-0000-000000000004','Track',99,2);
    INSERT INTO track_to_download VALUES
        ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000001'),
        ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000002');
    INSERT INTO quiz_packs(id,name) VALUES('00000000-0000-0000-0000-000000000006','Pack');
    INSERT INTO quiz_rounds(id,pack_id,number,type) VALUES
        ('00000000-0000-0000-0000-000000000007','00000000-0000-0000-0000-000000000006',1,'artist_by_track');
    INSERT INTO quiz_questions(id,round_id,number,artist_id,track_id,primary_text,
        sound_download_id,track_start_sec,track_finish_sec,answer) VALUES
        ('00000000-0000-0000-0000-000000000008','00000000-0000-0000-0000-000000000007',1,
         '00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000005','Track',
         '00000000-0000-0000-0000-000000000001',0,20,'Artist');
    INSERT INTO quiz_round_results(round_id,attempt_id,r,n,answers) VALUES
        ('00000000-0000-0000-0000-000000000007','00000000-0000-0000-0000-000000000009',1,1,ARRAY[true]);`

	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
}
