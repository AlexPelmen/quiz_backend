package httpapi_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

const feedbackAPI = "/api/v1/question-feedback"

type feedbackClient func(method, path, body string, want int) []byte

type feedbackClients struct {
	anonymous, player, moderator, admin feedbackClient
	playerID                            uuid.UUID
}

func newFeedbackClients(t *testing.T, pool *pgxpool.Pool, api http.Handler) feedbackClients {
	t.Helper()

	const password = "password-1234"

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}

	playerID := uuid.New()
	for _, user := range []struct {
		id          uuid.UUID
		login, role string
		permissions []string
	}{
		{playerID, "feedback-player", "player", []string{}},
		{uuid.New(), "feedback-admin", "admin", []string{}},
		{uuid.New(), "feedback-moderator", "moderator", []string{"question-feedback"}},
	} {
		_, err := pool.Exec(t.Context(), `INSERT INTO users(id,name,login,password_hash,role,permissions)
			VALUES($1,$2,$2,$3,$4,$5)`, user.id, user.login, hash, user.role, user.permissions)
		if err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.Handler(api, sessions, false)
	login := func(name string) feedbackClient {
		t.Helper()

		if name == "" {
			return feedbackHTTP(t, handler, nil)
		}

		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"login":%q,"password":%q}`, name, password)
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBufferString(body)))

		if w.Code != 200 || len(w.Result().Cookies()) == 0 {
			t.Fatalf("логин: %d %s", w.Code, w.Body.String())
		}

		return feedbackHTTP(t, handler, w.Result().Cookies()[0])
	}

	return feedbackClients{
		anonymous: login(""), player: login("feedback-player"), admin: login("feedback-admin"),
		moderator: login("feedback-moderator"), playerID: playerID,
	}
}

func feedbackHTTP(t *testing.T, handler http.Handler, cookie *http.Cookie) feedbackClient {
	return func(method, path, body string, want int) []byte {
		t.Helper()

		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != want {
			t.Fatalf("%s %s: %d вместо %d: %s", method, path, w.Code, want, w.Body.String())
		}

		return w.Body.Bytes()
	}
}

type feedbackFixture struct {
	feedbackClients
	pool *pgxpool.Pool
	uow  *store.UnitOfWork
}

func newFeedbackFixture(t *testing.T) feedbackFixture {
	t.Helper()

	pool, uow := testutil.Database(t)
	for i := range 10 {
		seedHTTPTrack(t, pool, i)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.Router(service.New(uow, logger, ""), logger)

	return feedbackFixture{feedbackClients: newFeedbackClients(t, pool, api), pool: pool, uow: uow}
}

func (f feedbackFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()

	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f feedbackFixture) pack(t *testing.T, kind string) domain.QuizPack {
	t.Helper()

	var pack domain.QuizPack

	body := fmt.Sprintf(`{"name":"Пакет","question_count":10,"round_types":[%q]}`, kind)
	decodeResult(t, f.admin("POST", "/api/v1/quiz-packs", body, 201), &pack)

	return pack
}

func (f feedbackFixture) reports(t *testing.T) []store.FeedbackReport {
	t.Helper()

	var listing struct {
		Items []store.FeedbackReport `json:"items"`
		Total int                    `json:"total"`
	}

	decodeResult(t, f.moderator("GET", feedbackAPI, "", 200), &listing)

	if listing.Total != len(listing.Items) {
		t.Fatalf("неверное количество жалоб: %+v", listing)
	}

	return listing.Items
}

func (f feedbackFixture) readyAudio(t *testing.T, q domain.Question) []byte {
	t.Helper()

	f.exec(t, `UPDATE quiz_questions SET status='ready',clip_name=id::text || '.mp3'`)

	dir := t.TempDir()
	f.admin("PATCH", "/api/v1/settings", fmt.Sprintf(`{"storage_dir":%q}`, dir), 200)

	clips := filepath.Join(dir, "music", "clips")
	if err := os.MkdirAll(clips, 0o700); err != nil {
		t.Fatal(err)
	}

	clip := []byte("проверочный клип")
	if err := os.WriteFile(filepath.Join(clips, q.ID.String()+".mp3"), clip, 0o600); err != nil {
		t.Fatal(err)
	}

	return clip
}

func TestQuestionFeedback(t *testing.T) {
	f := newFeedbackFixture(t)
	pack := f.pack(t, "artist_by_track")
	q := pack.Rounds[0].Questions[0]
	f.exec(t, `UPDATE artists SET yt_music_id='UCNEStAEX0fKYbZIXTAW8Juw' WHERE id=$1`, q.ArtistID)

	clip := f.readyAudio(t, q)
	items := checkFeedbackLists(t, f, q, clip)
	checkTrackReportActions(t, f, items, pack.Rounds[0].Questions[1].ArtistID)
}

func checkFeedbackLists(t *testing.T, f feedbackFixture, q domain.Question, clip []byte) []store.FeedbackReport {
	t.Helper()

	base := "/api/v1/questions/" + q.ID.String()
	f.anonymous("POST", base+"/feedback", `{"type":"bug","comment":"Ошибка"}`, 401)
	f.player("POST", base+"/feedback", `{"type":"bug","comment":"  "}`, 400)
	f.player("POST", base+"/feedback", `{"type":"like","comment":"Текст"}`, 400)

	wrongAuthor := fmt.Sprintf(`{"type":"bug","comment":"Ошибка","user_id":%q}`, uuid.New())
	f.player("POST", base+"/feedback", wrongAuthor, 400)
	f.player("POST", base+"/feedback", `{"type":"bad_audio","comment":"Плохой звук"}`, 204)
	f.player("POST", base+"/feedback", `{"type":"question_error","comment":"Проверьте трек"}`, 204)
	f.player("GET", feedbackAPI, "", 403)
	f.player("PUT", base+"/like", `{"liked":true}`, 204)
	f.player("PUT", base+"/like", `{"liked":true}`, 204)

	var state struct {
		Liked bool `json:"liked"`
	}

	decodeResult(t, f.player("GET", base+"/like", "", 200), &state)
	if !state.Liked {
		t.Fatal("лайк не сохранён")
	}

	items := f.reports(t)
	if len(items) != 2 || items[0].Comment != "Проверьте трек" || items[1].Comment != "Плохой звук" ||
		items[0].UserID != f.playerID || items[0].QuestionID != q.ID || items[0].ID == items[1].ID {
		t.Fatalf("ожидались две отдельные жалобы: %+v", items)
	}

	checkFeedbackLike(t, f, q.ID)

	path := feedbackAPI + "/" + items[0].ID.String()

	var detail store.FeedbackReport

	decodeResult(t, f.moderator("GET", path, "", 200), &detail)

	snapshot := detail.Question
	if snapshot.TrackName != "Трек" || snapshot.ArtistName != q.Answer || !snapshot.HasAudio ||
		snapshot.TrackFinishSec == nil || *snapshot.TrackFinishSec != 5 ||
		snapshot.YTMusicURL != "https://music.youtube.com/channel/UCNEStAEX0fKYbZIXTAW8Juw" {
		t.Fatalf("неполный снимок вопроса: %+v", snapshot)
	}

	f.player("GET", path+"/audio", "", 403)

	if got := f.moderator("GET", path+"/audio", "", 200); !bytes.Equal(got, clip) {
		t.Fatal("аудио жалобы отличается от игрового клипа")
	}

	return items
}

func checkFeedbackLike(t *testing.T, f feedbackFixture, question uuid.UUID) {
	t.Helper()

	var liked struct {
		Items []store.FeedbackLike `json:"items"`
		Total int                  `json:"total"`
	}

	decodeResult(t, f.admin("GET", feedbackAPI+"/likes", "", 200), &liked)

	if liked.Total != 1 || len(liked.Items) != 1 || liked.Items[0].ID != question || liked.Items[0].Likes != 1 {
		t.Fatalf("неверный список лайков: %+v", liked)
	}
}

func checkTrackReportActions(t *testing.T, f feedbackFixture, items []store.FeedbackReport, coartist uuid.UUID) {
	t.Helper()

	question := items[0].QuestionID
	base := "/api/v1/questions/" + question.String()
	path := feedbackAPI + "/" + items[0].ID.String()

	f.moderator("POST", path+"/actions/delete-track", `{}`, 403)
	f.admin("POST", path+"/actions/reset-artist", `{"yt_music_url":"https://example.org"}`, 400)
	f.admin("POST", path+"/actions/reject-cover", `{}`, 400)

	if len(f.reports(t)) != 2 {
		t.Fatal("ошибка модерации не должна удалять жалобу")
	}

	f.exec(t, `UPDATE artists SET name='Проверенный коллектив' WHERE id=$1`, coartist)
	f.admin("POST", path+"/actions/add-track-coauthor", fmt.Sprintf(`{"artist_id":%q}`, coartist), 204)

	var answer service.AnswerReveal

	body := fmt.Sprintf(`{"attempt_id":%q,"option_type":"artist","option_id":%q}`, uuid.New(), coartist)
	decodeResult(t, f.player("POST", base+"/answer", body, 201), &answer)

	if !answer.Correct {
		t.Fatal("соавтор трека должен приниматься как правильный ответ")
	}

	f.admin("GET", path, "", 404)
	f.player("POST", base+"/feedback", `{"type":"bug","comment":"Ещё одна жалоба"}`, 204)
	f.admin("POST", feedbackAPI+"/"+items[1].ID.String()+"/actions/delete-track", `{}`, 204)

	remaining := f.reports(t)
	if len(remaining) != 1 || !remaining[0].QuestionDeleted || remaining[0].Question.TrackName != "Трек" {
		t.Fatalf("снимок оставшейся жалобы утрачен после удаления трека: %+v", remaining)
	}

	path = feedbackAPI + "/" + remaining[0].ID.String()
	f.admin("GET", path+"/audio", "", 200)
	f.moderator("DELETE", path, "", 204)

	if len(f.reports(t)) != 0 {
		t.Fatal("обработанные жалобы должны удаляться")
	}

	checkFeedbackLike(t, f, question)
}

func TestFeedbackResetArtist(t *testing.T) {
	f := newFeedbackFixture(t)
	pack := f.pack(t, "artist_by_track")
	q := pack.Rounds[0].Questions[0]
	coartist := pack.Rounds[0].Questions[1].ArtistID

	if err := f.uow.Tracks.AddCoartist(t.Context(), *q.TrackID, coartist); err != nil {
		t.Fatal(err)
	}

	base := "/api/v1/questions/" + q.ID.String()
	f.player("POST", base+"/feedback", `{"type":"bug","comment":"Неверная ссылка исполнителя"}`, 204)

	path := feedbackAPI + "/" + f.reports(t)[0].ID.String() + "/actions/reset-artist"
	body := `{"yt_music_url":"https://music.youtube.com/@corrected"}`

	f.exec(t, `UPDATE artists SET status='processing' WHERE id=$1`, q.ArtistID)
	f.admin("POST", path, body, 409)
	f.exec(t, `UPDATE artists SET status='manual' WHERE id=$1`, q.ArtistID)
	f.admin("POST", path, body, 204)

	artist, err := f.uow.Artists.Get(t.Context(), q.ArtistID)
	if err != nil || artist.Status != "created" || artist.YTMusicURL != "https://music.youtube.com/@corrected" ||
		artist.YTMusicID != nil || artist.LogoDownloadID != nil {
		t.Fatalf("источник не сброшен: %+v %v", artist, err)
	}

	for _, table := range []string{"artist_to_track", "artist_to_album"} {
		var count int

		sql := "SELECT count(*) FROM " + table + " WHERE artist_id=$1"

		err := f.pool.QueryRow(t.Context(), sql, q.ArtistID).Scan(&count)
		if err != nil || count != 0 {
			t.Fatalf("связи с прежним каталогом остались в %s: %d, %v", table, count, err)
		}
	}

	shared, err := f.uow.Tracks.Get(t.Context(), *q.TrackID)
	if err != nil || shared.ArtistID != coartist {
		t.Fatalf("совместный трек должен остаться у второго исполнителя: %+v, %v", shared, err)
	}
}

func TestFeedbackArtistTags(t *testing.T) {
	f := newFeedbackFixture(t)
	pack := f.pack(t, "artist_by_track")
	q, other := pack.Rounds[0].Questions[0], pack.Rounds[0].Questions[1]

	f.exec(t, `UPDATE artists SET tags=ARRAY['rock'] WHERE id IN ($1,$2)`, q.ArtistID, other.ArtistID)

	questionPath := "/api/v1/questions/" + q.ID.String()
	f.player("POST", questionPath+"/feedback", `{"type":"question_error","comment":"Неверный жанр"}`, 204)

	reportPath := feedbackAPI + "/" + f.reports(t)[0].ID.String()
	path := reportPath + "/actions/edit-artist-tags"
	f.moderator("POST", path, `{"tags":[]}`, 403)
	f.admin("POST", path, `{}`, 400)
	f.admin("POST", path, `{"tags":["несуществующий-тег"]}`, 409)
	f.admin("GET", reportPath, "", 200)
	f.admin("POST", path, `{"tags":[]}`, 204)
	f.admin("GET", reportPath, "", 404)

	var artist, otherArtist domain.Artist

	decodeResult(t, f.admin("GET", "/api/v1/artists/"+q.ArtistID.String(), "", 200), &artist)
	decodeResult(t, f.admin("GET", "/api/v1/artists/"+other.ArtistID.String(), "", 200), &otherArtist)

	if len(artist.Tags) != 0 || len(otherArtist.Tags) != 1 || otherArtist.Tags[0] != "rock" {
		t.Fatalf("неверные теги после обработки жалобы: %v, %v", artist.Tags, otherArtist.Tags)
	}

	var tags []store.Tag

	decodeResult(t, f.admin("GET", "/api/v1/tags", "", 200), &tags)

	if len(tags) != 1 || tags[0].Name != "rock" {
		t.Fatalf("справочник тегов изменился: %+v", tags)
	}
}

func TestFeedbackCoverActions(t *testing.T) {
	f := newFeedbackFixture(t)
	cover := makeCoverFixture(t)

	f.exec(t, `UPDATE downloads SET uri=$1 WHERE source_type='image'`, cover)
	f.exec(t, `INSERT INTO album_guessing(album_id, cover_download_id, approved, rectangles)
		SELECT id,cover_download_id,true,
			'[{"x1":0,"y1":0,"x2":2500,"y2":5000},{"x1":7500,"y1":5000,"x2":10000,"y2":10000}]'
		FROM albums`)

	pack := f.pack(t, "artist_by_cover")
	questions := pack.Rounds[0].Questions
	base := "/api/v1/questions/" + questions[0].ID.String()

	f.player("POST", base+"/feedback", `{"type":"question_error","comment":"Совместный альбом"}`, 204)

	report := f.reports(t)[0]
	if len(report.Question.Albums) != 1 || !report.Question.HasImage {
		t.Fatalf("нет данных об обложке: %+v", report.Question)
	}

	path := feedbackAPI + "/" + report.ID.String()
	checkCoverPixels(t, f.admin("GET", path+"/image", "", 200), true)
	checkCoverPixels(t, f.admin("GET", path+"/answer-image", "", 200), false)

	albumID, coartist := report.Question.Albums[0].ID, questions[2].ArtistID
	coauthorPath := path + "/actions/add-album-coauthor"

	f.admin("POST", coauthorPath, fmt.Sprintf(`{"artist_id":%q,"album_id":%q}`, coartist, uuid.New()), 400)
	f.admin("POST", coauthorPath, fmt.Sprintf(`{"artist_id":%q,"album_id":%q}`, coartist, albumID), 204)

	if linked, err := f.uow.Albums.HasArtist(t.Context(), albumID, coartist); err != nil || !linked {
		t.Fatalf("соавтор альбома не сохранён: %v", err)
	}

	checkRejectCover(t, f, base, albumID)

	base = "/api/v1/questions/" + questions[1].ID.String()
	f.player("POST", base+"/feedback", `{"type":"question_error","comment":"Лишний исполнитель"}`, 204)
	f.admin("POST", feedbackAPI+"/"+f.reports(t)[0].ID.String()+"/actions/delete-artist", `{}`, 204)
	f.admin("GET", "/api/v1/artists/"+questions[1].ArtistID.String(), "", 404)
}

func checkRejectCover(t *testing.T, f feedbackFixture, questionPath string, albumID uuid.UUID) {
	t.Helper()

	f.player("POST", questionPath+"/feedback", `{"type":"question_error","comment":"На обложке подсказка"}`, 204)
	f.player("POST", questionPath+"/feedback", `{"type":"bug","comment":"Другая жалоба на обложку"}`, 204)

	items := f.reports(t)
	f.moderator("POST", feedbackAPI+"/"+items[1].ID.String()+"/actions/reject-cover", `{}`, 204)

	var approved bool

	err := f.pool.QueryRow(t.Context(), `SELECT approved FROM album_guessing WHERE album_id=$1`, albumID).Scan(&approved)
	if err != nil || approved {
		t.Fatalf("обложка не запрещена: approved=%t, %v", approved, err)
	}

	remaining := f.reports(t)
	if len(remaining) != 1 || !remaining[0].QuestionDeleted {
		t.Fatalf("вопрос с запрещённой обложкой остался в игре: %+v", remaining)
	}

	path := feedbackAPI + "/" + remaining[0].ID.String()
	checkCoverPixels(t, f.moderator("GET", path+"/image", "", 200), true)
	f.admin("DELETE", path, "", 204)
}
