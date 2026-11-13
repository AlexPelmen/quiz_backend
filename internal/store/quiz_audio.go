package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"devkeeper.com/quiz/internal/domain"
)

// UsesAudio сообщает, требует ли раунд готовой записи.
func (f PickFilter) UsesAudio() bool {
	switch f.Kind {
	case "artist_by_cover", "artist_by_logo", "artist_by_albums":
		return false
	default:
		return true
	}
}

// AudioTrack хранит данные рейтинга, нужные до выбора вопроса.
type AudioTrack struct {
	ID           uuid.UUID
	ArtistID     uuid.UUID
	ArtistName   string
	HasConsonant bool
}

// AudioTracks загружает доступные записи по убыванию прослушиваний.
func (r PackRepository) AudioTracks(ctx context.Context, tag string) ([]AudioTrack, error) {
	return query[AudioTrack](ctx, r.db, `SELECT t.id, a.id AS artist_id, a.name AS artist_name,
        quiz_title_has_consonant(t.name) AS has_consonant
        FROM tracks t JOIN artist_to_track owner ON owner.track_id=t.id
        JOIN artists a ON a.id=owner.artist_id
        WHERE NOT t.excluded_from_quiz AND NOT a.excluded_from_quiz
        AND a.status IN ('parsed','manual','partial','error') AND ($1='' OR $1=ANY(a.tags))
        AND EXISTS (`+finishedAudio+`)
        ORDER BY t.yt_plays DESC NULLS LAST, t.id, owner.is_primary DESC, a.id`, []any{tag})
}

// PickAudioTrack выбирает позицию из усечённого нормального распределения.
func (f PickFilter) PickAudioTrack(tracks []AudioTrack) (AudioTrack, bool) {
	if len(tracks) == 0 {
		return AudioTrack{}, false
	}

	// Совместная запись занимает одну позицию. Из подходящих участников
	// первым идёт основной исполнитель, если он есть среди кандидатов.
	count := 0
	for i, track := range tracks {
		if i == 0 || tracks[i-1].ID != track.ID {
			count++
		}
	}

	n := float64(count)
	position := (f.center() + 0.15*rand.NormFloat64()) * n
	for position < 0 || position >= n {
		position = (f.center() + 0.15*rand.NormFloat64()) * n
	}

	index := int(position)
	for i, track := range tracks {
		if i > 0 && tracks[i-1].ID == track.ID {
			continue
		}
		if index == 0 {
			return track, true
		}
		index--
	}

	return AudioTrack{}, false
}

// PickAudio выбирает трек из рейтинга с учётом ограничений вопроса.
func (r PackRepository) PickAudio(
	ctx context.Context, tracks []AudioTrack, excluded []uuid.UUID, filter PickFilter,
) (domain.Candidate, error) {
	eligible := make([]AudioTrack, 0, len(tracks))
	for _, track := range tracks {
		if !slices.Contains(excluded, track.ArtistID) && (filter.Kind != "track_vowels" || track.HasConsonant) {
			eligible = append(eligible, track)
		}
	}

	track, ok := filter.PickAudioTrack(eligible)
	if !ok {
		return domain.Candidate{}, domain.ErrNotFound
	}

	return r.AudioCandidate(ctx, track)
}

// AudioCandidate получает данные выбранного трека и предпочтительную запись.
func (r PackRepository) AudioCandidate(ctx context.Context, track AudioTrack) (domain.Candidate, error) {
	c := domain.Candidate{Artist: domain.Artist{ID: track.ArtistID, Name: track.ArtistName}}
	err := r.db.QueryRow(ctx, `SELECT to_jsonb(t) || jsonb_build_object('artist_id',$2::uuid),
        COALESCE(to_jsonb(al),'{}'::jsonb), to_jsonb(sound.d)
        FROM tracks t LEFT JOIN albums al ON al.id=t.album_id
        JOIN LATERAL (`+finishedAudio+`
            ORDER BY d.preview,(d.source_type='upload') DESC,
            CASE WHEN d.source_type='upload' THEN d.created_at END DESC,d.created_at,d.id LIMIT 1) sound ON true
        WHERE t.id=$1`, track.ID, track.ArtistID).Scan(&c.Track, &c.Album, &c.Sound)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}

	return c, err
}
