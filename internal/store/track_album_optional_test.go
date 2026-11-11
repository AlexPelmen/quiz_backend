package store_test

import (
	"testing"

	"devkeeper.com/quiz/internal/testutil"
	"github.com/google/uuid"
)

func TestTrackWithoutAlbumCanBeComplete(t *testing.T) {
	pool, _ := testutil.Database(t)
	ctx := t.Context()
	artist, track, download := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO artists(id,name,provider_parsed) VALUES($1,'Artist',true)`, artist); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO tracks(id,name,yt_plays,duration_seconds) VALUES($1,'Song',10,100)`, track); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO artist_to_track(track_id,artist_id) VALUES($1,$2)`, track, artist); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO downloads(id,source_type,file_type,status) VALUES($1,'upload','mp3','finished')`, download); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO track_to_download(track_id,download_id) VALUES($1,$2)`, track, download); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var missing []string
	if err := pool.QueryRow(ctx, `SELECT status,missing_fields FROM catalog_tracks WHERE id=$1`, track).Scan(&status, &missing); err != nil {
		t.Fatal(err)
	}
	if status != "parsed" || len(missing) != 0 {
		t.Fatalf("status=%q missing=%v", status, missing)
	}
}
