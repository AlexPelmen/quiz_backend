package store

import (
	"context"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

const approvedCover = `JOIN downloads d ON d.id=al.cover_download_id AND d.status='finished'
	JOIN album_guessing g ON g.album_id=al.id AND g.cover_download_id=d.id AND g.approved`

// GuessingList возвращает обложки с готовыми файлами и актуальным решением модерации.
func (r AlbumRepository) GuessingList(
	ctx context.Context, page domain.Page, shuffle string,
) ([]domain.AlbumGuessing, error) {
	order := "artist_name,album_name,album_id"
	args := []any{page.Status, page.Limit, page.Offset}

	if shuffle != "" {
		// Сначала по одной обложке на исполнителя, затем по второй и так далее.
		order = `row_number() OVER (PARTITION BY artist_id ORDER BY md5($4::text || ':' || album_id::text),album_id),
			md5($4::text || ':' || artist_id::text),artist_id`

		args = append(args, shuffle)
	}

	return query[domain.AlbumGuessing](ctx, r.db, `
		SELECT album_id,album_name,artist_name,cover_download_id,status,rectangles FROM (
		SELECT al.id AS album_id, owner.artist_id, al.name AS album_name, a.name AS artist_name, d.id AS cover_download_id,
			CASE WHEN g.approved THEN 'approved' WHEN g.approved=false THEN 'rejected' ELSE 'pending' END AS status,
			COALESCE(g.rectangles,'[]'::jsonb) AS rectangles
		FROM albums al JOIN artist_to_album owner ON owner.album_id=al.id JOIN artists a ON a.id=owner.artist_id
		JOIN downloads d ON d.id=al.cover_download_id AND d.status='finished'
		LEFT JOIN album_guessing g ON g.album_id=al.id AND g.cover_download_id=d.id
	) covers WHERE ($1='' OR status=$1) ORDER BY `+order+` LIMIT $2 OFFSET $3`, args)
}

// GuessingDecision хранит решение для конкретной версии обложки.
type GuessingDecision struct {
	CoverID    uuid.UUID
	Approved   bool
	Rectangles []domain.CoverRectangle
}

// SaveGuessing записывает решение, если на альбоме всё ещё установлена эта обложка.
func (r AlbumRepository) SaveGuessing(ctx context.Context, albumID uuid.UUID, d GuessingDecision) error {
	return execute(ctx, r.db, `INSERT INTO album_guessing(album_id,cover_download_id,approved,rectangles)
		SELECT al.id,d.id,$3,$4 FROM albums al JOIN downloads d ON d.id=al.cover_download_id
		WHERE al.id=$1 AND d.id=$2 AND d.status='finished'
		ON CONFLICT(album_id) DO UPDATE SET cover_download_id=EXCLUDED.cover_download_id,
			approved=EXCLUDED.approved,rectangles=EXCLUDED.rectangles`,
		[]any{albumID, d.CoverID, d.Approved, d.Rectangles})
}

type guessingImage struct {
	CoverDownloadID uuid.UUID
	Rectangles      []domain.CoverRectangle
}

// QuestionCover находит актуальную одобренную обложку только для вопроса по альбому.
func (r PackRepository) QuestionCover(
	ctx context.Context, packID uuid.UUID, number int,
) (uuid.UUID, []domain.CoverRectangle, error) {
	cover, err := one[guessingImage](ctx, r.db, `SELECT d.id AS cover_download_id,g.rectangles
		FROM quiz_rounds qr JOIN quiz_questions q ON q.round_id=qr.id JOIN albums al ON al.id=q.album_id
		`+approvedCover+` WHERE qr.pack_id=$1 AND q.number=$2 AND qr.type='artist_by_cover'`, []any{packID, number})

	return cover.CoverDownloadID, cover.Rectangles, err
}
