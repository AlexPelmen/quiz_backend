package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// PackRepository хранит пакеты и выбирает кандидатов для раундов.
type PackRepository struct{ db DB }

// Pick выбирает исполнителя, альбом и трек за три запроса.
func (r PackRepository) Pick(ctx context.Context, excluded []uuid.UUID, filter PickFilter) (domain.Candidate, error) {
	var c domain.Candidate

	args := []any{excluded, filter.Tag, filter.Kind}
	// Экспоненциальная гонка даёт вероятность Points/sum(Points), без копирования каталога в память.
	artist, err := one[domain.Artist](ctx, r.db, `SELECT a.* FROM artists a
		WHERE a.points>0 AND a.status='parsed' AND NOT(a.id=ANY($1::uuid[]))
		AND ($2='' OR $2=ANY(a.tags)) AND EXISTS(SELECT 1 FROM albums al JOIN tracks t ON t.album_id=al.id
		WHERE al.artist_id=a.id AND t.artist_id=a.id AND al.points>0 AND t.points>0 AND `+eligible+`)
		ORDER BY -ln(GREATEST(random(),1e-15))/a.points LIMIT 1`, args)
	if err != nil {
		return c, err
	}

	c.Artist = artist

	al, err := one[domain.Album](ctx, r.db, `SELECT al.* FROM albums al JOIN artists a ON a.id=al.artist_id
		WHERE al.artist_id=$1 AND al.points>0 AND EXISTS(SELECT 1 FROM tracks t
		WHERE t.album_id=al.id AND t.artist_id=a.id AND t.points>0 AND `+eligibleAlbum+`)
		ORDER BY -ln(GREATEST(random(),1e-15))/al.points LIMIT 1`, []any{artist.ID, filter.Kind})
	if err != nil {
		return c, err
	}

	c.Album = al

	return r.pickTrack(ctx, c, filter.Kind)
}

// PickFilter задаёт тип раунда и необязательный тег исполнителя.
type PickFilter struct {
	Kind string
	Tag  string
}

const eligible = `CASE $3 WHEN 'artist_by_cover' THEN
	EXISTS(SELECT 1 FROM downloads d WHERE d.id=al.cover_download_id AND d.status='finished')
	WHEN 'artist_by_logo' THEN
	EXISTS(SELECT 1 FROM downloads d WHERE d.id=a.logo_download_id AND d.status='finished')
	ELSE EXISTS(SELECT 1 FROM track_to_download td JOIN downloads d ON d.id=td.download_id
	WHERE td.track_id=t.id AND d.status='finished' AND d.file_type='mp3'
	AND (d.meta->>'duration_seconds')::double precision>0) END`

const eligibleAlbum = `CASE $2 WHEN 'artist_by_cover' THEN
	EXISTS(SELECT 1 FROM downloads d WHERE d.id=al.cover_download_id AND d.status='finished')
	WHEN 'artist_by_logo' THEN
	EXISTS(SELECT 1 FROM downloads d WHERE d.id=a.logo_download_id AND d.status='finished')
	ELSE EXISTS(SELECT 1 FROM track_to_download td JOIN downloads d ON d.id=td.download_id
	WHERE td.track_id=t.id AND d.status='finished' AND d.file_type='mp3'
	AND (d.meta->>'duration_seconds')::double precision>0) END`

func (r PackRepository) pickTrack(ctx context.Context, c domain.Candidate, kind string) (domain.Candidate, error) {
	err := r.db.QueryRow(ctx, `SELECT to_jsonb(t),sound.data FROM tracks t
		LEFT JOIN LATERAL (SELECT to_jsonb(d) AS data FROM downloads d
		JOIN track_to_download td ON td.download_id=d.id WHERE td.track_id=t.id
		AND d.status='finished' AND d.file_type='mp3'
		AND (d.meta->>'duration_seconds')::double precision>0 ORDER BY d.created_at,d.id LIMIT 1) sound ON true
		WHERE t.album_id=$1 AND t.artist_id=$2 AND t.points>0
		AND ($3 IN ('artist_by_cover','artist_by_logo') OR sound.data IS NOT NULL)
		ORDER BY -ln(GREATEST(random(),1e-15))/t.points LIMIT 1`,
		c.Album.ID, c.Artist.ID, kind).Scan(&c.Track, &c.Sound)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}

	return c, err
}

// Create атомарно сохраняется вызывающим Unit of Work вместе с раундами.
func (r PackRepository) Create(ctx context.Context, p domain.QuizPack) error {
	if err := execute(ctx, r.db, `INSERT INTO quiz_packs(id,name,description,tags) VALUES($1,$2,$3,$4)`,
		[]any{p.ID, p.Name, p.Description, p.Tags}); err != nil {
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

	return nil
}

func (r PackRepository) createQuestion(ctx context.Context, q domain.Question) error {
	return execute(ctx, r.db, `INSERT INTO quiz_questions(id,round_id,number,artist_id,track_id,
		primary_text,secondary_text,image_download_id,sound_download_id,track_start_sec,track_finish_sec,answer,answer_info)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, []any{
		q.ID, q.RoundID, q.Number,
		q.ArtistID, q.TrackID, q.PrimaryText, q.SecondaryText, q.ImageDownloadID, q.SoundDownloadID,
		q.TrackStartSec, q.TrackFinishSec, q.Answer, q.AnswerInfo,
	})
}

// List возвращает пакеты без вложенных вопросов.
func (r PackRepository) List(ctx context.Context, p domain.Page) ([]domain.QuizPack, error) {
	return query[domain.QuizPack](ctx, r.db, `SELECT * FROM quiz_packs
		ORDER BY created_at DESC,id LIMIT $1 OFFSET $2`, []any{p.Limit, p.Offset})
}

// Get загружает пакет, его раунды и вопросы с сохранением порядка.
func (r PackRepository) Get(ctx context.Context, id uuid.UUID) (domain.QuizPack, error) {
	p, err := one[domain.QuizPack](ctx, r.db, "SELECT * FROM quiz_packs WHERE id=$1", []any{id})
	if err != nil {
		return p, err
	}

	p.Rounds, err = query[domain.Round](ctx, r.db,
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
func (r PackRepository) Question(ctx context.Context, packID uuid.UUID, number int) (domain.Question, error) {
	return one[domain.Question](ctx, r.db, `SELECT q.* FROM quiz_questions q JOIN quiz_rounds r ON r.id=q.round_id
		WHERE r.pack_id=$1 AND q.number=$2`, []any{packID, number})
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
