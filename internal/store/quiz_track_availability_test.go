package store_test

import (
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
	"github.com/google/uuid"
)

func TestQuizUsesReadyTrackOutsidePopularList(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	artist, popular, ready, download := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO artists(id,name,status,provider_parsed) VALUES($1,'Artist','parsed',true)`, []any{artist}},
		{`INSERT INTO tracks(id,name,yt_plays,duration_seconds) VALUES($1,'Popular',1000,120),($2,'Older',100,120)`, []any{popular, ready}},
		{`INSERT INTO artist_to_track(track_id,artist_id) VALUES($1,$3),($2,$3)`, []any{popular, ready, artist}},
		{`INSERT INTO artist_track_shortlists(artist_id,track_ids) VALUES($1,ARRAY[$2]::uuid[])`, []any{artist, popular}},
		{`INSERT INTO downloads(id,source_type,file_type,status,meta) VALUES($1,'upload','mp3','finished','{"duration_seconds":120}')`, []any{download}},
		{`INSERT INTO track_to_download(track_id,download_id) VALUES($1,$2)`, []any{ready, download}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	filter := store.PickFilter{Kind: "artist_by_track", Center: 0.5}
	picked, err := uow.Packs.Pick(ctx, []uuid.UUID{}, filter)
	if err != nil || picked.Artist.ID != artist || picked.Track.ID != ready {
		t.Fatalf("подбор: %+v, %v", picked, err)
	}
	artists, err := uow.Packs.AudioArtists(ctx, []uuid.UUID{}, filter)
	if err != nil || len(artists) != 1 || artists[0].ID != artist {
		t.Fatalf("исполнители: %+v, %v", artists, err)
	}
	direct, err := uow.Packs.PickTrack(ctx, domain.Candidate{Artist: picked.Artist}, filter)
	if err != nil || direct.Track.ID != ready {
		t.Fatalf("трек: %+v, %v", direct, err)
	}
}
