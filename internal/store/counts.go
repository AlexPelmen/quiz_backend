package store

import (
	"context"
	"devkeeper.com/quiz/internal/domain"
)

// ListCount возвращает число записей списка с учётом фильтров.
func (u *UnitOfWork) ListCount(ctx context.Context, resource string, p domain.Page) (int64, error) {
	var q string
	var args []any
	switch resource {
	case "artists":
		q = `SELECT count(*) FROM catalog_artists WHERE ($1='' OR status=$1) AND ($2='' OR $2=ANY(tags)) AND ($3='' OR strpos(lower(name),lower($3))>0)`
		args = []any{p.Status, p.Tag, p.Name}
	case "albums":
		q = `SELECT count(*) FROM catalog_albums WHERE ($1::uuid IS NULL OR EXISTS (SELECT 1 FROM artist_to_album link WHERE link.album_id=catalog_albums.id AND link.artist_id=$1)) AND ($2='' OR status=$2)`
		args = []any{p.ArtistID, p.Status}
	case "tracks":
		q = `SELECT count(*) FROM catalog_tracks WHERE (artist_id=$1 OR ($1::uuid IS NULL AND ` + primaryTrackArtist + `)) AND ($2::uuid IS NULL OR album_id=$2) AND ($3='' OR status=$3) AND ($4='' OR audio_status=$4)`
		args = []any{p.ArtistID, p.AlbumID, p.Status, p.AudioStatus}
	case "downloads":
		q = `SELECT count(*) FROM downloads d WHERE ($1='' OR d.status=$1) AND ($2='' OR ($2='mp3' AND d.file_type='mp3') OR ($2='image' AND d.file_type<>'mp3')) AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM track_to_download td WHERE td.download_id=d.id AND td.track_id=$3))`
		args = []any{p.Status, p.FileType, p.TrackID}
	case "album-guessing":
		q = `SELECT count(*) FROM albums al JOIN artist_to_album owner ON owner.album_id=al.id AND owner.is_primary JOIN artists a ON a.id=owner.artist_id JOIN downloads d ON d.id=al.cover_download_id AND d.status='finished' LEFT JOIN album_guessing g ON g.album_id=al.id AND g.cover_download_id=d.id WHERE ($1='' OR CASE WHEN g.approved THEN 'approved' WHEN g.approved=false THEN 'rejected' ELSE 'pending' END=$1)`
		args = []any{p.Status}
	case "album-cleanup":
		q = `SELECT count(*) FROM (` + cleanupCandidates + `) candidates`
		args = []any{domain.AlbumSubscriberThresholds()}
	case "quiz-packs":
		q = `SELECT count(*) FROM quiz_packs`
	default:
		return 0, domain.ErrNotFound
	}
	var count int64
	err := u.db.QueryRow(ctx, q, args...).Scan(&count)
	return count, err
}

// DownloadActiveCount возвращает число заданий в очереди и обработке.
func (u *UnitOfWork) DownloadActiveCount(ctx context.Context, kind string) (int64, error) {
	var count int64
	err := u.db.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE status IN ('pending','processing') AND (($1='mp3' AND file_type='mp3') OR ($1='image' AND file_type<>'mp3'))`, kind).Scan(&count)
	return count, err
}
