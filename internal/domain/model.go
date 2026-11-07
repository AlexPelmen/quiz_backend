// Package domain описывает каталог, фоновые задания и квиз-пакеты.
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Ошибки предметной области используются HTTP-слоем для выбора кода ответа.
var (
	ErrNotFound     = errors.New("объект не найден")
	ErrConflict     = errors.New("конфликт состояния")
	ErrInvalid      = errors.New("некорректные данные")
	ErrInsufficient = errors.New("недостаточно данных для полного раунда")
)

// Artist хранит исполнителя; Artists содержит имена участников группы.
type Artist struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	FullName       string     `json:"full_name"`
	Country        string     `json:"country"`
	Status         string     `json:"status"`
	LogoDownloadID *uuid.UUID `json:"logo_download_id"`
	Tags           []string   `json:"tags"`
	Artists        []string   `json:"artists"`
	Points         int        `json:"points"`
	GeniusID       *int64     `json:"genius_id"`
	MusicBrainzID  string     `json:"musicbrainz_id"`
	ManualFields   []string   `json:"manual_fields"`
	Error          string     `json:"error"`
	LeaseUntil     *time.Time `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GeniusQuota содержит последнее наблюдение лимита официального API Genius.
type GeniusQuota struct {
	Limit      int64     `json:"limit" db:"quota_limit"`
	Remaining  int64     `json:"remaining"`
	ResetAt    time.Time `json:"reset_at"`
	ObservedAt time.Time `json:"observed_at"`
}

// Album содержит полный треклист CSV независимо от числа импортированных Track.
type Album struct {
	ID                uuid.UUID  `json:"id"`
	Name              string     `json:"name"`
	CoverDownloadID   *uuid.UUID `json:"cover_download_id"`
	ReleaseDate       string     `json:"release_date"`
	ArtistID          uuid.UUID  `json:"artist_id"`
	Points            int        `json:"points"`
	GeniusID          *int64     `json:"genius_id"`
	TrackList         string     `json:"track_list"`
	TrackListComplete bool       `json:"track_list_complete"`
	ManualReleaseDate bool       `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Track хранится даже при отсутствии доступной записи.
type Track struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	ArtistID  uuid.UUID  `json:"artist_id"`
	AlbumID   *uuid.UUID `json:"album_id"`
	Points    int        `json:"points"`
	GeniusID  *int64     `json:"genius_id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TrackWithStatus дополняет трек состоянием его аудиозаписей.
type TrackWithStatus struct {
	Track
	Status string `json:"status"`
}

// Download описывает источник и результат загрузки. URI назначается только сервером.
type Download struct {
	ID            uuid.UUID      `json:"id"`
	SourceType    string         `json:"source_type"`
	SourceURI     string         `json:"source_uri"`
	URI           string         `json:"uri"`
	Status        string         `json:"status"`
	FileType      string         `json:"file_type"`
	Meta          map[string]any `json:"meta"`
	Error         string         `json:"error"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt time.Time      `json:"next_attempt_at"`
	LeaseUntil    *time.Time     `json:"-"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// ParseRun позволяет отслеживать явно запущенный импорт.
type ParseRun struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	Processed  int        `json:"processed"`
	Failed     int        `json:"failed"`
	Error      string     `json:"error"`
	LeaseUntil *time.Time `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// QuizPack объединяет упорядоченные раунды.
type QuizPack struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Rounds      []Round   `json:"rounds,omitempty" db:"-"`
}

// Round содержит ровно десять вопросов одного типа.
type Round struct {
	ID        uuid.UUID  `json:"id"`
	PackID    uuid.UUID  `json:"pack_id"`
	Number    int        `json:"number"`
	Type      string     `json:"type"`
	Questions []Question `json:"questions" db:"-"`
}

// Question фиксирует границы звучания исходной записи в секундах.
type Question struct {
	ID              uuid.UUID      `json:"id"`
	RoundID         uuid.UUID      `json:"round_id"`
	Number          int            `json:"number"`
	ArtistID        uuid.UUID      `json:"artist_id"`
	TrackID         uuid.UUID      `json:"track_id"`
	PrimaryText     string         `json:"primary_text"`
	SecondaryText   string         `json:"secondary_text"`
	ImageDownloadID *uuid.UUID     `json:"image_download_id"`
	SoundDownloadID *uuid.UUID     `json:"sound_download_id"`
	TrackStartSec   *float64       `json:"trackStartSec"`
	TrackFinishSec  *float64       `json:"trackFinishSec"`
	Answer          string         `json:"answer"`
	AnswerInfo      map[string]any `json:"answer_info"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Candidate содержит данные, необходимые обработчику раунда.
type Candidate struct {
	Artist Artist
	Album  Album
	Track  Track
	Sound  *Download
}

// Page задаёт ограничения выборки каталога.
type Page struct {
	Limit    int
	Offset   int
	ArtistID *uuid.UUID
	AlbumID  *uuid.UUID
	TrackID  *uuid.UUID
	Status   string
	Tag      string
	Name     string
	Sort     string
	Order    string
}
