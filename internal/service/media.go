package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
)

// Media выполняет ограниченные по времени операции над локальными записями.
type Media struct{}

var errInvalidMP3 = errors.New("не удалось прочитать MP3 с положительной длительностью")

func runCommand(ctx context.Context, executable string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	// Утилиты запускаются напрямую, без shell: аргументы не интерпретируются как команды.
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s: %w: %.2048s", filepath.Base(executable), err, exitErr.Stderr)
		}

		return nil, fmt.Errorf("%s: %w", filepath.Base(executable), err)
	}

	return output, nil
}

func probe(ctx context.Context, executable, path string) (float64, error) {
	output, err := runCommand(ctx, executable, []string{
		"-v", "error", "-f", "mp3", "-protocol_whitelist", "file",
		"-select_streams", "a:0", "-show_entries", "format=duration:stream=codec_name", "-of", "json", path,
	})
	if err != nil {
		return 0, err
	}

	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return 0, err
	}

	if len(result.Streams) == 0 || result.Streams[0].CodecName != "mp3" {
		return 0, errInvalidMP3
	}

	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0, errInvalidMP3
	}

	return duration, nil
}

func fetchFile(ctx context.Context, s domain.Settings, address, path string) (result error) {
	if err := domain.ValidateURL(address); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", s.UserAgent)

	client := &http.Client{Timeout: time.Duration(s.TaskTimeoutSeconds) * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("источник файла: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, f.Close()) }()

	n, err := io.Copy(f, io.LimitReader(resp.Body, s.MaxDownloadBytes+1))
	if err != nil {
		return err
	}

	if n > s.MaxDownloadBytes {
		return errors.New("файл превышает max_download_bytes")
	}

	return nil
}

func downloadYouTube(ctx context.Context, s domain.Settings, d domain.Download, path string) error {
	u, err := url.Parse(d.SourceURI)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return fmt.Errorf("%w: требуется HTTPS-ссылка YouTube", domain.ErrInvalid)
	}

	host := strings.ToLower(u.Hostname())
	if host != "youtube.com" && host != "www.youtube.com" && host != "m.youtube.com" && host != "youtu.be" {
		return fmt.Errorf("%w: неподдерживаемый хост YouTube", domain.ErrInvalid)
	}

	_, err = runCommand(ctx, s.YTDLP, []string{
		"--ignore-config", "--js-runtimes", s.YTDLPJSRuntime,
		"--no-playlist", "--no-progress", "--no-warnings", "--no-part", "--no-mtime",
		"--max-filesize", strconv.FormatInt(s.MaxDownloadBytes, 10),
		"--format", "bestaudio/best", "--output", path, "--", d.SourceURI,
	})

	return err
}

func tagValue(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

func convertMP3(ctx context.Context, s domain.Settings, input string, output domain.Download) error {
	_, err := runCommand(ctx, s.FFmpeg, []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", input,
		"-map", "0:a:0", "-vn", "-map_metadata", "-1", "-c:a", "libmp3lame", "-b:a", "192k",
		"-id3v2_version", "3", "-metadata", "title=" + tagValue(output.Meta, "title"),
		"-metadata", "artist=" + tagValue(output.Meta, "artist"),
		"-metadata", "album=" + tagValue(output.Meta, "album"), "-f", "mp3", output.URI,
	})

	return err
}

func downloadFile(ctx context.Context, s domain.Settings, d *domain.Download, directory string) (result error) {
	root, err := filepath.Abs(s.StorageDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}

	work, err := os.MkdirTemp(root, ".download-")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.RemoveAll(work)) }()

	input := filepath.Join(work, "input")
	if d.SourceType == "youtube" {
		err = downloadYouTube(ctx, s, *d, input)
	} else {
		err = fetchFile(ctx, s, d.SourceURI, input)
	}

	if err != nil {
		return err
	}

	if err := checkSize(input, s.MaxDownloadBytes); err != nil {
		return err
	}

	if d.FileType == "mp3" {
		d.URI = filepath.Join(work, "result.mp3")
		if err := convertMP3(ctx, s, input, *d); err != nil {
			return err
		}

		duration, err := probe(ctx, s.FFprobe, d.URI)
		if err != nil {
			return err
		}

		d.Meta["duration_seconds"] = duration
		input = d.URI
	} else {
		kind, err := checkImage(input)
		if err != nil {
			return err
		}

		d.FileType = kind
	}

	if err := checkSize(input, s.MaxDownloadBytes); err != nil {
		return err
	}

	targetDir := filepath.Join(root, filepath.FromSlash(directory))
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(targetDir, d.ID.String()+"."+d.FileType)
	if err := os.Rename(input, path); err != nil {
		return err
	}

	d.URI = path

	return nil
}

func checkSize(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.Size() == 0 || info.Size() > maxBytes {
		return errors.New("недопустимый размер файла")
	}

	return nil
}

func checkImage(path string) (kind string, result error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer func() { result = errors.Join(result, f.Close()) }()

	buffer := make([]byte, 512)

	n, err := f.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	kinds := map[string]string{
		"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif",
		"image/webp": "webp", "image/bmp": "bmp", "image/x-icon": "ico",
	}

	kind, ok := kinds[http.DetectContentType(buffer[:n])]
	if !ok {
		return "", errors.New("источник не вернул поддерживаемое изображение")
	}

	return kind, nil
}

// Audio возвращает путь к кэшированному отрывку по номеру вопроса в пакете.
func (a *App) Audio(ctx context.Context, packID uuid.UUID, number int) (string, error) {
	q, err := a.uow.Packs.Question(ctx, packID, number)
	if err != nil {
		return "", err
	}

	if q.SoundDownloadID == nil || q.TrackStartSec == nil || q.TrackFinishSec == nil {
		return "", domain.ErrNotFound
	}

	d, err := a.uow.Downloads.Get(ctx, *q.SoundDownloadID)
	if err != nil {
		return "", err
	}

	if d.Status != "finished" {
		return "", domain.ErrNotFound
	}

	s, err := a.Settings(ctx)
	if err != nil {
		return "", err
	}

	return a.media.clip(ctx, s, clipInput{Question: q, Download: d})
}

type clipInput struct {
	Question domain.Question
	Download domain.Download
}

func (m *Media) clip(ctx context.Context, s domain.Settings, in clipInput) (path string, result error) {
	q, d := in.Question, in.Download
	dir := filepath.Join(s.StorageDir, "music", "clips")

	path = filepath.Join(dir, q.ID.String()+".mp3")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}

	work, err := os.MkdirTemp(dir, ".clip-")
	if err != nil {
		return "", err
	}

	defer func() { result = errors.Join(result, os.RemoveAll(work)) }()

	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	tmp := filepath.Join(work, "clip.mp3")
	// Перекодирование обеспечивает точную нарезку; копирование MP3-пакетов даёт неточные границы.
	_, err = runCommand(ctx, s.FFmpeg, []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(*q.TrackStartSec, 'f', 6, 64), "-i", d.URI,
		"-t", strconv.FormatFloat(*q.TrackFinishSec-*q.TrackStartSec, 'f', 6, 64),
		"-map", "0:a:0", "-vn", "-map_metadata", "-1", "-c:a", "libmp3lame", "-b:a", "192k",
		"-metadata", fmt.Sprintf("title=Вопрос %d", q.Number), "-f", "mp3", tmp,
	})
	if err != nil {
		return "", err
	}

	if err := os.Rename(tmp, path); err != nil {
		// Другой запрос мог первым сохранить тот же неизменяемый отрывок.
		if _, statErr := os.Stat(path); statErr != nil {
			return "", errors.Join(err, statErr)
		}
	}

	return path, nil
}

// DownloadPath разрешает выдачу только готовых файлов, адресованных по UUID.
func (a *App) DownloadPath(ctx context.Context, id uuid.UUID) (string, error) {
	d, err := a.uow.Downloads.Get(ctx, id)
	if err != nil {
		return "", err
	}

	if d.Status != "finished" || d.URI == "" {
		return "", domain.ErrNotFound
	}

	return d.URI, nil
}

// DownloadForm задаёт источник ручной загрузки либо оставляет прежний для повторной попытки.
type DownloadForm struct {
	SourceType string `json:"source_type"`
	SourceURI  string `json:"source_uri"`
}

// RetryDownload повторяет ошибку или назначает источник ручной задаче.
func (a *App) RetryDownload(ctx context.Context, id uuid.UUID, f DownloadForm) (domain.Download, error) {
	d, err := a.uow.Downloads.Get(ctx, id)
	if err != nil {
		return d, err
	}

	if d.Status != "manual" && d.Status != "errored" {
		return d, domain.ErrConflict
	}

	if f.SourceType != "" {
		d.SourceType = f.SourceType
	}

	if f.SourceURI != "" {
		d.SourceURI = f.SourceURI
	}

	if d.SourceType != "youtube" && d.SourceType != "mp3" && d.SourceType != "image" {
		return d, fmt.Errorf("%w: нужен источник youtube, mp3 или image", domain.ErrInvalid)
	}

	if (d.FileType == "mp3") == (d.SourceType == "image") {
		return d, fmt.Errorf("%w: источник не соответствует типу файла", domain.ErrInvalid)
	}

	if err := domain.ValidateURL(d.SourceURI); err != nil {
		return d, err
	}

	return a.uow.Downloads.Retry(ctx, d)
}
