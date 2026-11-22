package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// PackRepository хранит пакеты и выбирает кандидатов для раундов.
type PackRepository struct{ db DB }

// Pick выбирает исполнителя и нужный для типа раунда медиаобъект.
func (r PackRepository) Pick(ctx context.Context, excluded []uuid.UUID, filter PickFilter) (domain.Candidate, error) {
	if filter.UsesAudio() {
		tracks, err := r.AudioTracks(ctx, filter.Tag)
		if err != nil {
			return domain.Candidate{}, err
		}
		return r.PickAudio(ctx, tracks, excluded, filter)
	}

	var c domain.Candidate

	sql, args := rankCandidates(`SELECT a.* FROM catalog_artists a
        WHERE NOT a.excluded_from_quiz AND a.status IN ('parsed','manual','partial','error') AND NOT(a.id=ANY($1::uuid[]))
        AND ($2='' OR $2=ANY(a.tags)) AND CASE $3
        WHEN 'artist_by_cover' THEN EXISTS(SELECT 1 FROM catalog_albums al `+approvedCover+`
            WHERE al.artist_id=a.id AND NOT al.excluded_from_quiz)
        WHEN 'artist_by_logo' THEN EXISTS(SELECT 1 FROM downloads d
            WHERE d.id=a.logo_download_id AND d.status='finished')
        ELSE (SELECT count(DISTINCT al.name) FROM catalog_albums al
            WHERE al.artist_id=a.id AND NOT al.excluded_from_quiz AND strpos(lower(al.name),lower(a.name))=0)>=2 END`,
		"e.*", filter, []any{excluded, filter.Tag, filter.Kind})

	artist, err := one[domain.Artist](ctx, r.db, sql+" LIMIT 1", args)
	if err != nil {
		return c, err
	}

	c.Artist = artist

	switch filter.Kind {
	case "artist_by_cover":
		sql, args = rankCandidates(`SELECT al.* FROM catalog_albums al `+approvedCover+`
            WHERE al.artist_id=$1 AND NOT al.excluded_from_quiz`, "e.*", filter, []any{artist.ID})
		c.Album, err = one[domain.Album](ctx, r.db, sql+" LIMIT 1", args)

		return c, err
	case "artist_by_logo":
		return c, nil
	case "artist_by_albums":
		sql, args = rankCandidates(`SELECT DISTINCT ON (name) id,name,yt_plays,asked,guessed
            FROM catalog_albums WHERE artist_id=$1 AND NOT excluded_from_quiz AND strpos(lower(name),lower($2))=0
            ORDER BY name,yt_plays DESC NULLS LAST,id`, "e.id,e.name", filter, []any{artist.ID, artist.Name})
		err = r.db.QueryRow(ctx, "SELECT array_agg(name),array_agg(id) FROM ("+sql+" LIMIT 10) picked", args...).
			Scan(&c.AlbumNames, &c.AlbumIDs)

		return c, err
	default:
		return c, domain.ErrInvalid
	}
}

// PickFilter задаёт ограничения и конфигурацию выбора по позициям.
type PickFilter struct {
	Kind             string
	Tag              string
	Center           float64
	CenterSet        bool
	GuessRateEnabled bool
	AskedThreshold   int64
}

func (f PickFilter) center() float64 {
	if f.Center == 0 && !f.CenterSet {
		return 0.5
	}

	return f.Center
}

func rankCandidates(eligible, columns string, filter PickFilter, args []any) (string, []any) {
	// Экспоненциальная гонка даёт взвешенный случайный порядок без повторов.
	sql := fmt.Sprintf(`WITH eligible AS MATERIALIZED (%s), scored AS (
        SELECT id,CASE WHEN $%d::boolean AND asked>$%d::bigint AND asked>0 THEN guessed::double precision/asked
            ELSE COALESCE(yt_plays::double precision/NULLIF(max(yt_plays) OVER (),0),0) END AS score
        FROM eligible
    ), ranked AS (
        SELECT id,row_number() OVER (ORDER BY score DESC,random()) AS pos,count(*) OVER () AS n FROM scored
    ) SELECT %s FROM eligible e JOIN ranked r ON r.id=e.id
        ORDER BY -ln(GREATEST(random(),1e-15))/exp(-0.5*power((r.pos-$%d::double precision*r.n)/(0.15*r.n),2))`,
		eligible, len(args)+1, len(args)+2, columns, len(args)+3)

	return sql, append(args, filter.GuessRateEnabled, filter.AskedThreshold, filter.center())
}

const finishedAudio = `SELECT d FROM track_to_download td JOIN downloads d ON d.id=td.download_id
    WHERE td.track_id=t.id AND d.status='finished' AND d.file_type='mp3'
    AND (d.meta->>'duration_seconds')::double precision>0`

// Create атомарно сохраняется вызывающим Unit of Work вместе с раундами.
func (r PackRepository) Create(ctx context.Context, p domain.QuizPack) error {
	if err := execute(ctx, r.db, `INSERT INTO quiz_packs(id,name,description,tags,difficulty,cover_download_id)
		VALUES($1,$2,$3,$4,COALESCE(NULLIF($5,''),'medium'),$6)`,
		[]any{p.ID, p.Name, p.Description, p.Tags, p.Difficulty, p.CoverDownloadID}); err != nil {
		return err
	}

	for _, round := range p.Rounds {
		if err := execute(ctx, r.db, `INSERT INTO quiz_rounds(id,pack_id,number,type) VALUES($1,$2,$3,$4)`,
			[]any{round.ID, p.ID, round.Number, round.Type}); err != nil {
			return err
		}

		for _, q := range round.Questions {
			if err := r.createQuestion(ctx, q); err != nil {
				return err
			}
		}
	}

	return r.updateClipStatus(ctx, p.ID, "")
}

func (r PackRepository) createQuestion(ctx context.Context, q domain.Question) error {
	_, err := r.db.Exec(ctx, `WITH saved AS (
		INSERT INTO quiz_questions(id,round_id,number,artist_id,track_id,album_id,primary_text,secondary_text,
			image_download_id,sound_download_id,track_start_sec,track_finish_sec,answer,answer_info,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,CASE WHEN $10::uuid IS NULL THEN 'ready' ELSE 'new' END)
		RETURNING id
	) INSERT INTO quiz_question_album_refs(question_id,album_id)
	SELECT saved.id,refs.id FROM saved CROSS JOIN unnest($15::uuid[]) AS refs(id)`,
		q.ID, q.RoundID, q.Number, q.ArtistID, q.TrackID, q.AlbumID, q.PrimaryText, q.SecondaryText,
		q.ImageDownloadID, q.SoundDownloadID, q.TrackStartSec, q.TrackFinishSec, q.Answer, q.AnswerInfo, q.AlbumRefs)

	return translate(err)
}

const packWithClipCounts = `SELECT * FROM quiz_pack_status p`

// List возвращает пакеты без вложенных вопросов.
func (r PackRepository) List(ctx context.Context, p domain.Page) ([]domain.QuizPack, error) {
	return query[domain.QuizPack](ctx, r.db, packWithClipCounts+`
		WHERE ($3='' OR p.status=$3) ORDER BY p.created_at DESC,p.id LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset, p.Status})
}

// Get загружает пакет, его раунды и вопросы с сохранением порядка.
func (r PackRepository) Get(ctx context.Context, id uuid.UUID) (domain.QuizPack, error) {
	p, err := one[domain.QuizPack](ctx, r.db, packWithClipCounts+" WHERE p.id=$1", []any{id})
	if err != nil {
		return p, err
	}

	p.Rounds, err = query[domain.QuizRound](ctx, r.db,
		"SELECT * FROM quiz_rounds WHERE pack_id=$1 ORDER BY number", []any{id})
	if err != nil {
		return p, err
	}

	for i := range p.Rounds {
		p.Rounds[i].Questions, err = query[domain.Question](ctx, r.db,
			"SELECT * FROM quiz_questions WHERE round_id=$1 ORDER BY number", []any{p.Rounds[i].ID})
		if err != nil {
			return p, err
		}
	}

	return p, nil
}

// Question возвращает вопрос по сквозному номеру внутри пакета.
func (r PackRepository) Question(
	ctx context.Context, packID uuid.UUID, number int, readyOnly bool,
) (domain.Question, error) {
	return one[domain.Question](ctx, r.db, `SELECT q.* FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id
		JOIN quiz_pack_status p ON p.id=r.pack_id
		WHERE r.pack_id=$1 AND q.number=$2 AND NOT q.deleted AND (NOT $3::boolean OR p.status='ready')`,
		[]any{packID, number, readyOnly})
}

// Update изменяет описание сохранённого пакета.
func (r PackRepository) Update(ctx context.Context, p domain.QuizPack) error {
	return execute(ctx, r.db, `UPDATE quiz_packs SET name=$2,description=$3,tags=$4,updated_at=now() WHERE id=$1`,
		[]any{p.ID, p.Name, p.Description, p.Tags})
}

// Delete удаляет пакет вместе с раундами и вопросами.
func (r PackRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return execute(ctx, r.db, "DELETE FROM quiz_packs WHERE id=$1", []any{id})
}
