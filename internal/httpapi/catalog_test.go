package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestUploadedMP3BecomesPreferred(t *testing.T) {
	pool, uow := testutil.Database(t)

	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("для проверки MP3 требуется ffprobe")
	}

	path := httpAudioFixture(t)
	if path == "" {
		t.Skip("для проверки MP3 требуется ffmpeg")
	}

	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	settings := domain.DefaultSettings()
	settings.StorageDir = t.TempDir()

	if err := uow.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}

	seedHTTPTrack(t, pool, 1)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.Router(service.New(uow, logger, ""), logger)
	call := client(t, router)

	var tracks []domain.TrackWithStatus

	decodeResult(t, call("GET", "/api/v1/tracks?audio_status=finished", "", 200), &tracks)

	if len(tracks) != 1 || tracks[0].AudioStatus != "finished" {
		t.Fatalf("не найден трек с готовой записью: %+v", tracks)
	}

	trackPath := "/api/v1/tracks/" + tracks[0].ID.String() + "/audio"
	old := uploadRecording(t, router, trackPath, audio)
	latest := uploadRecording(t, router, trackPath, audio)

	candidate, err := uow.Packs.Pick(ctx, []uuid.UUID{}, store.PickFilter{Kind: "artist_by_track"})
	if err != nil || candidate.Sound == nil || candidate.Sound.ID != latest.ID {
		t.Fatalf("ожидалась последняя загруженная запись: %+v, %v", candidate.Sound, err)
	}

	got := call("GET", "/api/v1/downloads/"+old.ID.String()+"/file", "", 200)
	if !bytes.Equal(got, audio) {
		t.Fatal("предыдущая запись стала недоступна после замены")
	}
}

func TestArtistDeletionWithLinkedCatalog(t *testing.T) {
	pool, uow := testutil.Database(t)
	seedHTTPTrack(t, pool, 1)

	var artistID uuid.UUID
	if err := pool.QueryRow(t.Context(), "SELECT id FROM artists").Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	call("DELETE", "/api/v1/artists/"+artistID.String(), "", 204)

	var artists, albums, tracks, files int

	err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM artists), (SELECT count(*) FROM albums),
        (SELECT count(*) FROM tracks), (SELECT count(*) FROM downloads)`).Scan(&artists, &albums, &tracks, &files)
	if err != nil || artists != 0 || albums != 0 || tracks != 0 || files != 2 {
		t.Fatalf("связанные записи: %d исполнителей, %d альбомов, %d треков, %d файлов, %v",
			artists, albums, tracks, files, err)
	}
}

func TestCatalogDeletionPreservesPackQuestions(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	audio, covers, hints := seedDeletionPacks(t, pool, call)
	first, second, third := audio.Rounds[0].Questions[0], audio.Rounds[0].Questions[1], audio.Rounds[0].Questions[2]

	var album uuid.UUID

	if err := pool.QueryRow(ctx, "SELECT album_id FROM tracks WHERE id=$1", third.TrackID).Scan(&album); err != nil {
		t.Fatal(err)
	}

	task, err := uow.ClaimQuizClip(ctx, 60)
	if err != nil || task.Question.ID != first.ID {
		t.Fatalf("первый клип: %+v, %v", task, err)
	}

	if err := uow.FinishQuizClip(ctx, task, "", "ошибка подготовки"); err != nil {
		t.Fatal(err)
	}

	for _, entry := range []struct {
		resource string
		id       uuid.UUID
	}{
		{"artists", first.ArtistID},
		{"tracks", *second.TrackID},
		{"albums", album},
	} {
		path := fmt.Sprintf("/api/v1/%s/%s", entry.resource, entry.id)
		call("DELETE", path, "", 204)
		call("GET", path, "", 404)

		var archived bool

		err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM deleted_catalog_entries WHERE resource=$1 AND id=$2
		)`, entry.resource, entry.id).Scan(&archived)
		if err != nil || !archived {
			t.Fatalf("идентификатор не архивирован: %s, %v", path, err)
		}
	}

	after := assertRetiredQuestions(t, call, audio, func(q domain.Question) bool { return q.Number <= 3 })
	shouldDelete := func(q domain.Question) bool { return q.ArtistID == first.ArtistID || q.ArtistID == third.ArtistID }

	assertRetiredQuestions(t, call, covers, shouldDelete)
	assertRetiredQuestions(t, call, hints, shouldDelete)

	if after.ClipError != "" || after.ClipsTotal != 7 {
		t.Fatalf("удалённый клип остался в очереди: %+v", after)
	}

	call("GET", fmt.Sprintf("/api/v1/quiz-packs/%s/questions/%d/audio", audio.ID, first.Number), "", 404)

	task, err = uow.ClaimQuizClip(ctx, 60)
	if err != nil || task.Question.Number != 4 {
		t.Fatalf("очередь не возобновилась: %+v, %v", task, err)
	}

	submitRoundWithRetiredQuestions(t, call, after.Rounds[0])

	var asked int

	err = pool.QueryRow(ctx, "SELECT asked FROM artists WHERE id=$1", second.ArtistID).Scan(&asked)
	if err != nil || asked != 0 {
		t.Fatalf("удалённый вопрос попал в статистику: %d, %v", asked, err)
	}

	call("POST", "/api/v1/artists", fmt.Sprintf(`{"name":%q}`, first.Answer), 201)
}

func seedDeletionPacks(
	t *testing.T, pool *pgxpool.Pool, call func(string, string, string, int) []byte,
) (domain.QuizPack, domain.QuizPack, domain.QuizPack) {
	t.Helper()

	for i := range 10 {
		seedHTTPTrack(t, pool, i)
	}

	if _, err := pool.Exec(t.Context(), `
		INSERT INTO album_guessing(album_id,cover_download_id,approved,rectangles)
		SELECT id,cover_download_id,true,'[]' FROM albums;
		WITH extra AS (
			SELECT id AS artist_id,gen_random_uuid() AS album_id FROM artists
		), saved AS (
			INSERT INTO albums(id,name) SELECT album_id,'Второй альбом' FROM extra RETURNING id
		) INSERT INTO artist_to_album(album_id,artist_id)
		SELECT saved.id,extra.artist_id FROM saved JOIN extra ON extra.album_id=saved.id;
	`); err != nil {
		t.Fatal(err)
	}

	generate := func(kind string) domain.QuizPack {
		var pack domain.QuizPack

		body := fmt.Sprintf(`{"name":%q,"question_count":10,"round_types":[%q]}`, kind, kind)
		decodeResult(t, call("POST", "/api/v1/quiz-packs", body, 201), &pack)

		return pack
	}

	return generate("artist_by_track"), generate("artist_by_cover"), generate("artist_by_albums")
}

func assertRetiredQuestions(
	t *testing.T, call func(string, string, string, int) []byte,
	before domain.QuizPack, shouldDelete func(domain.Question) bool,
) domain.QuizPack {
	t.Helper()

	var after domain.QuizPack

	decodeResult(t, call("GET", "/api/v1/quiz-packs/"+before.ID.String(), "", 200), &after)

	if len(after.Rounds) != 1 || len(after.Rounds[0].Questions) != 10 {
		t.Fatalf("изменился состав раунда: %+v", after)
	}

	for i, q := range after.Rounds[0].Questions {
		saved := before.Rounds[0].Questions[i]
		if q.ID != saved.ID || q.Number != saved.Number || q.Deleted != shouldDelete(saved) {
			t.Fatalf("изменился слот вопроса: %+v", q)
		}

		if q.Deleted && (q.PrimaryText != "Вопрос был удалён" || q.ArtistID != uuid.Nil || q.TrackID != nil ||
			q.Answer != "" || q.SecondaryText != "" || len(q.AnswerInfo) != 0 ||
			q.ImageDownloadID != nil || q.SoundDownloadID != nil || q.TrackStartSec != nil || q.TrackFinishSec != nil) {
			t.Fatalf("в удалённом вопросе остались данные: %+v", q)
		}
	}

	return after
}

func submitRoundWithRetiredQuestions(
	t *testing.T, call func(string, string, string, int) []byte, round domain.QuizRound,
) {
	t.Helper()

	payload := roundPayload{AttemptID: uuid.New(), Total: 10, Correct: 7, Answers: map[uuid.UUID]bool{}}
	for _, q := range round.Questions {
		payload.Answers[q.ID] = !q.Deleted
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	call("POST", fmt.Sprintf("/api/v1/quiz-rounds/%s/results", round.ID), string(body), 204)
}

func TestArtistYTMusicURL(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))
	var artist domain.Artist
	decodeResult(t, call("POST", "/api/v1/artists", `{"name":"Avantasia","yt_music_url":"https://music.youtube.com/@avantasiaofficial"}`, 201), &artist)
	if artist.YTMusicURL != "https://music.youtube.com/@avantasiaofficial" {
		t.Fatalf("ссылка не сохранена: %q", artist.YTMusicURL)
	}
	path := "/api/v1/artists/" + artist.ID.String()
	call("PATCH", path, `{"yt_music_url":"https://example.com/@artist"}`, 400)
	call("PATCH", path, `{"yt_music_url":""}`, 200)
	if _, err := pool.Exec(t.Context(), "UPDATE artists SET status='parsed' WHERE id=$1", artist.ID); err != nil {
		t.Fatal(err)
	}
	decodeResult(t, call("PATCH", path, `{"yt_music_url":"https://music.youtube.com/@avantasiaofficial"}`, 200), &artist)
	if artist.Status != "created" {
		t.Fatalf("исполнитель не поставлен в очередь: %q", artist.Status)
	}
	call("PATCH", path, `{"yt_music_url":""}`, 200)
	if _, err := pool.Exec(t.Context(), "UPDATE artists SET yt_music_id=$1 WHERE id=$2", "UCNEStAEX0fKYbZIXTAW8Juw", artist.ID); err != nil {
		t.Fatal(err)
	}
	decodeResult(t, call("GET", path, "", 200), &artist)
	if artist.YTMusicURL != "https://music.youtube.com/channel/UCNEStAEX0fKYbZIXTAW8Juw" {
		t.Fatalf("ссылка по ID не сформирована: %q", artist.YTMusicURL)
	}
	call("PATCH", path, `{"yt_music_url":"https://music.youtube.com/@other"}`, 400)
}

func uploadRecording(t *testing.T, router http.Handler, path string, audio []byte) domain.Download {
	t.Helper()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(audio))
	req.Header.Set("Content-Type", "application/octet-stream")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("загрузка MP3: %d %s", recorder.Code, recorder.Body.String())
	}

	var uploaded domain.Download

	decodeResult(t, recorder.Body.Bytes(), &uploaded)

	return uploaded
}
