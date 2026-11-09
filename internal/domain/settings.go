package domain

import (
	"fmt"
	"net/url"
)

// Settings содержит эксплуатационную конфигурацию без секретов.
type Settings struct {
	CoverArtArchiveURL          string   `json:"cover_art_archive_url"`
	HitmoURL                    string   `json:"hitmo_url"`
	YTMusicPython               string   `json:"yt_music_python"`
	QuizGuessRateEnabled        bool     `json:"quiz_guess_rate_enabled"`
	QuizGuessRateAskedThreshold int64    `json:"quiz_guess_rate_asked_threshold"`
	PollSeconds                 int      `json:"poll_seconds"`
	HTTPTimeoutSeconds          int      `json:"http_timeout_seconds"`
	TaskTimeoutSeconds          int      `json:"task_timeout_seconds"`
	DownloadWorkers             int      `json:"download_workers"`
	MaxDownloadBytes            int64    `json:"max_download_bytes"`
	StorageDir                  string   `json:"storage_dir"`
	FFmpeg                      string   `json:"ffmpeg"`
	FFprobe                     string   `json:"ffprobe"`
	YTDLP                       string   `json:"yt_dlp"`
	YTDLPJSRuntime              string   `json:"yt_dlp_js_runtime"`
	GeniusURL                   string   `json:"genius_url"`
	GeniusWebURL                string   `json:"genius_web_url"`
	MusicBrainzURL              string   `json:"musicbrainz_url"`
	UserAgent                   string   `json:"user_agent"`
	RoundTypes                  []string `json:"round_types"`
	GeniusRequestIntervalMS     int      `json:"genius_request_interval_ms"`
	Source429Retries            int      `json:"source_429_retries"`
	Source429DelaySeconds       int      `json:"source_429_delay_seconds"`
	Source429MaxDelaySeconds    int      `json:"source_429_max_delay_seconds"`
	ParserStopFailures          int      `json:"parser_stop_failures"`
}

// DefaultSettings задаёт рабочие значения для нового экземпляра.
func DefaultSettings() Settings {
	return Settings{
		CoverArtArchiveURL: "https://coverartarchive.org",
		HitmoURL:           "https://rus.hitmos.fm", YTMusicPython: "python3",
		QuizGuessRateEnabled: true, QuizGuessRateAskedThreshold: 5, PollSeconds: 2, HTTPTimeoutSeconds: 30,
		TaskTimeoutSeconds: 600, DownloadWorkers: 2, MaxDownloadBytes: 100 << 20,
		StorageDir: "data", FFmpeg: "ffmpeg", FFprobe: "ffprobe", YTDLP: "yt-dlp", YTDLPJSRuntime: "node",
		GeniusURL: "https://api.genius.com", GeniusWebURL: "https://genius.com/api",
		MusicBrainzURL: "https://musicbrainz.org/ws/2", UserAgent: "MusicQuiz/1.0 (local music quiz service)",
		GeniusRequestIntervalMS: 1000, Source429Retries: 3,
		Source429DelaySeconds: 1, Source429MaxDelaySeconds: 3, ParserStopFailures: 10,
		RoundTypes: []string{
			"artist_by_track", "artist_by_cover", "track_vowels", "artist_by_logo", "artist_initial",
			"artist_by_track", "artist_by_cover", "track_vowels", "artist_by_logo", "artist_initial",
		},
	}
}

// Validate проверяет конфигурацию до её записи и использования.
func (s Settings) Validate() error {
	if s.ParserStopFailures < 2 || s.ParserStopFailures > 1000 ||
		s.GeniusRequestIntervalMS < 100 || s.GeniusRequestIntervalMS > 60000 ||
		s.Source429Retries < 0 || s.Source429Retries > 10 || s.Source429DelaySeconds < 1 ||
		s.Source429MaxDelaySeconds < s.Source429DelaySeconds || s.Source429MaxDelaySeconds > 3600 {
		return fmt.Errorf("%w: недопустимые настройки ожидания источника", ErrInvalid)
	}

	if s.QuizGuessRateAskedThreshold < 0 || s.PollSeconds < 1 || s.HTTPTimeoutSeconds < 1 ||
		s.TaskTimeoutSeconds < s.HTTPTimeoutSeconds || s.TaskTimeoutSeconds > 86400 ||
		s.DownloadWorkers < 1 || s.DownloadWorkers > 16 || s.MaxDownloadBytes < 1024 {
		return fmt.Errorf("%w: недопустимые числовые настройки", ErrInvalid)
	}

	if s.YTMusicPython == "" || s.StorageDir == "" || s.FFmpeg == "" || s.FFprobe == "" ||
		s.YTDLP == "" || s.UserAgent == "" {
		return fmt.Errorf("%w: пути и user_agent обязательны", ErrInvalid)
	}

	if len(s.RoundTypes) != 10 {
		return fmt.Errorf("%w: требуется десять типов раундов", ErrInvalid)
	}

	if s.YTDLPJSRuntime != "node" && s.YTDLPJSRuntime != "deno" && s.YTDLPJSRuntime != "bun" {
		return fmt.Errorf("%w: yt_dlp_js_runtime должен быть node, deno или bun", ErrInvalid)
	}

	for _, kind := range s.RoundTypes {
		if !RoundTypeValid(kind) {
			return fmt.Errorf("%w: неизвестный тип раунда %q", ErrInvalid, kind)
		}
	}

	for _, address := range []string{s.GeniusURL, s.GeniusWebURL, s.MusicBrainzURL, s.HitmoURL, s.CoverArtArchiveURL} {
		if err := ValidateURL(address); err != nil {
			return err
		}
	}

	return nil
}

// RoundTypeValid проверяет наличие обработчика указанного типа.
func RoundTypeValid(kind string) bool {
	switch kind {
	case "artist_by_track", "artist_by_cover", "track_vowels", "artist_by_logo", "artist_initial",
		"artist_by_albums", "artist_cities", "artist_growth", "artist_alphabet":
		return true
	default:
		return false
	}
}

// ValidateURL разрешает только абсолютные HTTP(S)-адреса без учётных данных.
func ValidateURL(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: требуется абсолютный HTTP(S) URL", ErrInvalid)
	}

	return nil
}
