package httpapi_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestQuizAnswerFlow(t *testing.T) {
	pool, uow := testutil.Database(t)
	seedHTTPTrack(t, pool, 1)
	seedHTTPTrack(t, pool, 2)

	fixture := quizAnswerFixture(t, pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.Router(service.New(uow, logger, ""), logger)
	player := quizAnswerPlayer(t, pool, router)
	call := client(t, player)

	options := assertAnswerOptions(t, call)

	var hidden domain.QuizPack

	packURL := "/api/v1/quiz-packs/" + fixture.pack.String()
	decodeResult(t, call("GET", packURL, "", 200), &hidden)

	for _, q := range hidden.Rounds[0].Questions {
		if q.Answer != "" || q.AnswerInfo != nil || q.ArtistID != uuid.Nil || q.TrackID != nil {
			t.Fatalf("игровой пакет раскрывает ответ: %+v", q)
		}
	}

	attempt := uuid.New()
	roundBody := fmt.Sprintf(`{"attempt_id":%q}`, attempt)
	roundURL := "/api/v1/quiz-rounds/" + fixture.round.String() + "/results"
	firstURL := "/api/v1/questions/" + fixture.first.String() + "/answer"
	secondURL := "/api/v1/questions/" + fixture.second.String() + "/answer"

	call("POST", roundURL, roundBody, 400)
	call("POST", firstURL, fmt.Sprintf(`{"attempt_id":%q,"text":"motley crue"}`, attempt), 400)
	call("POST", firstURL, fmt.Sprintf(`{"attempt_id":%q}`, attempt), 400)
	call("POST", firstURL, answerSelection(attempt, uuid.New()), 400)
	call("POST", firstURL, fmt.Sprintf(
		`{"attempt_id":%q,"option_id":%q,"option_type":"artist","text":"Произвольный текст"}`, attempt, options.correct,
	), 400)

	var reveal service.AnswerReveal

	wrong := answerSelection(attempt, options.other)
	decodeResult(t, call("POST", firstURL, wrong, 201), &reveal)

	if reveal.Correct || reveal.Question.Answer != "Mötley Crüe" || reveal.Text != "Mötley Light" {
		t.Fatalf("неверный результат проверки: %+v", reveal)
	}

	correct := answerSelection(attempt, options.correct)
	decodeResult(t, call("POST", firstURL, correct, 201), &reveal)

	if reveal.Correct || reveal.Text != "Mötley Light" {
		t.Fatalf("повтор не должен менять ответ: %+v", reveal)
	}

	call("POST", roundURL, roundBody, 400)

	decodeResult(t, call("POST", secondURL, answerSelection(attempt, options.other), 201), &reveal)

	if !reveal.Correct || reveal.Question.Answer != "Mötley Light" {
		t.Fatalf("правильный вариант не засчитан: %+v", reveal)
	}

	call("POST", roundURL, roundBody, 204)
	call("POST", roundURL, roundBody, 204)
	assertSavedQuizScore(t, pool, fixture.round, attempt)

	correct = answerSelection(uuid.New(), options.correct)
	decodeResult(t, call("POST", firstURL, correct, 201), &reveal)

	if !reveal.Correct {
		t.Fatalf("правильная подсказка не засчитана: %+v", reveal)
	}

	timeoutAttempt := uuid.New()
	timeout := fmt.Sprintf(`{"attempt_id":%q,"timed_out":true}`, timeoutAttempt)
	call("POST", firstURL, fmt.Sprintf(`{"attempt_id":%q,"timed_out":true,"option_id":%q,"option_type":"artist"}`,
		timeoutAttempt, options.correct), 400)
	decodeResult(t, call("POST", firstURL, timeout, 201), &reveal)
	if !reveal.TimedOut || reveal.Correct || reveal.Text != "" || reveal.Question.Answer != "Mötley Crüe" {
		t.Fatalf("после истечения времени должен раскрыться ответ без начисления очка: %+v", reveal)
	}

	decodeResult(t, call("POST", firstURL, answerSelection(timeoutAttempt, options.correct), 201), &reveal)
	if !reveal.TimedOut || reveal.Correct {
		t.Fatalf("поздний ответ не должен менять сохранённый пропуск: %+v", reveal)
	}

	decodeResult(t, call("POST", secondURL, answerSelection(timeoutAttempt, options.other), 201), &reveal)
	if reveal.TimedOut || !reveal.Correct {
		t.Fatalf("пропуск не должен мешать ответить на следующий вопрос: %+v", reveal)
	}

	call("POST", roundURL, fmt.Sprintf(`{"attempt_id":%q}`, timeoutAttempt), 204)
	assertSavedQuizScore(t, pool, fixture.round, timeoutAttempt)
	assertAnswerTypos(t, pool, call, fixture.first)
}

func assertAnswerTypos(
	t *testing.T, pool *pgxpool.Pool, call func(string, string, string, int) []byte, question uuid.UUID,
) {
	t.Helper()

	path := "/api/v1/questions/" + question.String() + "/answer"
	for _, test := range []struct {
		name string
		want bool
	}{
		{"Mötlee Crüe", true},
		{"Möttley Crüe", true},
		{"Mötley Crü", true},
		{"Mxtley Crxe", false},
	} {
		option := uuid.New()
		if _, err := pool.Exec(t.Context(), "INSERT INTO artists(id,name) VALUES($1,$2)", option, test.name); err != nil {
			t.Fatal(err)
		}

		var reveal service.AnswerReveal

		decodeResult(t, call("POST", path, answerSelection(uuid.New(), option), 201), &reveal)

		if reveal.Correct != test.want {
			t.Fatalf("ответ %q: correct=%t, ожидалось %t", test.name, reveal.Correct, test.want)
		}
	}

	option := uuid.New()

	if _, err := pool.Exec(t.Context(), "UPDATE quiz_questions SET answer='Ghost' WHERE id=$1", question); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), "INSERT INTO artists(id,name) VALUES($1,'Ghast')", option); err != nil {
		t.Fatal(err)
	}

	var reveal service.AnswerReveal

	decodeResult(t, call("POST", path, answerSelection(uuid.New(), option), 201), &reveal)

	if reveal.Correct {
		t.Fatal("ответ из пяти букв не должен засчитываться с опечаткой")
	}
}

func answerSelection(attempt, option uuid.UUID) string {
	return fmt.Sprintf(`{"attempt_id":%q,"option_id":%q,"option_type":"artist"}`, attempt, option)
}

type quizOptionIDs struct {
	correct, other uuid.UUID
}

func assertAnswerOptions(t *testing.T, call func(string, string, string, int) []byte) quizOptionIDs {
	t.Helper()

	const search = "/api/v1/quiz-answer-options?q="

	call("GET", "/api/v1/artists", "", 403)

	var options []domain.AnswerOption

	decodeResult(t, call("GET", search+"otley&prefer=track", "", 200), &options)

	if len(options) != 4 || options[0].Type != "track" || options[0].Artist != "Mötley Crüe" {
		t.Fatalf("неверные подсказки: %+v", options)
	}

	var ids quizOptionIDs

	for _, option := range options {
		switch option.Name {
		case "Mötley Crüe":
			ids.correct = option.ID
		case "Mötley Light":
			ids.other = option.ID
		}
	}

	decodeResult(t, call("GET", search+"ot", "", 200), &options)

	if len(options) != 0 {
		t.Fatalf("короткий поиск должен проверять только начало: %+v", options)
	}

	decodeResult(t, call("GET", search+url.QueryEscape("арк"), "", 200), &options)

	if len(options) != 2 || options[0].Name != "Аркан" || options[1].Name != "Arctic Monkeys" {
		t.Fatalf("совпадение в исходном написании должно идти первым: %+v", options)
	}

	for _, test := range []struct{ query, want string }{
		{"motley crue", "Mötley Crüe"},
		{"Mo\u0308tley Cru\u0308e", "Mötley Crüe"},
		{"mxtley crue", "Mötley Crüe"},
		{"motley crx", "Mötley Crüe"},
		{"motley   part 2", "Mötley — Part Two"},
		{"motley, volume two", "Mötley / Volume 2"},
		{"мотлей part 2", "Mötley — Part Two"},
		{"мотлей volume two", "Mötley / Volume 2"},
		{"арктик", "Arctic Monkeys"},
		{"korol i shut", "Король и Шут"},
		{"пласебо", "Placebo"},
		{"2 steps from hell", "Two Steps from Hell"},
		{"2", "Two Steps from Hell"},
		{"ac dc", "AC/DC"},
		{"blink 182", "Blink-182"},
		{"u2", "U2"},
		{"!!!", "!!!"},
		{"%", "%"},
		{"x", "X"},
		{"йод", "Йод"},
	} {
		decodeResult(t, call("GET", search+url.QueryEscape(test.query), "", 200), &options)

		if len(options) != 1 || options[0].Name != test.want {
			t.Fatalf("поиск %q: %+v, ожидалось %q", test.query, options, test.want)
		}
	}

	return ids
}

func assertSavedQuizScore(t *testing.T, pool *pgxpool.Pool, round, attempt uuid.UUID) {
	t.Helper()

	var correctCount int

	err := pool.QueryRow(t.Context(), `SELECT r FROM quiz_round_results WHERE round_id=$1 AND attempt_id=$2`,
		round, attempt).Scan(&correctCount)
	if err != nil || correctCount != 1 {
		t.Fatalf("итог раунда: %d, ошибка: %v", correctCount, err)
	}
}

func quizAnswerPlayer(t *testing.T, pool *pgxpool.Pool, router http.Handler) http.Handler {
	t.Helper()

	hash, err := auth.HashPassword("test-player-password")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO users(id,name,login,password_hash,role)
		VALUES($1,'Игрок','answer-player',$2,'player')`, uuid.New(), hash); err != nil {
		t.Fatal(err)
	}

	authorization, err := auth.New(pool)
	if err != nil {
		t.Fatal(err)
	}

	handler := auth.Handler(router, authorization, false)
	login := httptest.NewRecorder()
	payload := strings.NewReader(`{"login":"answer-player","password":"test-player-password"}`)
	handler.ServeHTTP(login, httptest.NewRequest("POST", "/api/v1/auth/login", payload))

	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("вход: %d %s", login.Code, login.Body.String())
	}

	cookie := login.Result().Cookies()[0]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		handler.ServeHTTP(w, r)
	})
}

type quizAnswerIDs struct {
	pack, round, first, second uuid.UUID
}

func quizAnswerFixture(t *testing.T, pool *pgxpool.Pool) quizAnswerIDs {
	t.Helper()

	if _, err := pool.Exec(t.Context(), `
		UPDATE artists SET name='Mötley Crüe' WHERE name='Исполнитель 1';
		UPDATE artists SET name='Mötley Light' WHERE name='Исполнитель 2';
		INSERT INTO artists(id,name)
		SELECT gen_random_uuid(), v.name FROM (
			VALUES ('X'),('Йод'),('Иод'),('AC/DC'),('Two Steps from Hell'),('Blink-182'),('U2'),('!!!'),('!!!!'),('%'),
				('Arctic Monkeys'),('Аркан'),('Король и Шут'),('Placebo')
		) v(name);
		UPDATE albums SET name='Mötley — Part Two' WHERE id IN (
			SELECT album_id FROM artist_to_album aa JOIN artists a ON a.id=aa.artist_id WHERE a.name='Mötley Crüe'
		);
		UPDATE tracks SET name='Mötley / Volume 2' WHERE id IN (
			SELECT track_id FROM artist_to_track at JOIN artists a ON a.id=at.artist_id WHERE a.name='Mötley Crüe'
		)`); err != nil {
		t.Fatal(err)
	}

	ids := quizAnswerIDs{pack: uuid.New(), round: uuid.New(), first: uuid.New(), second: uuid.New()}

	if _, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO quiz_packs(id,name) VALUES($1,'Квиз') RETURNING id),
		r AS (INSERT INTO quiz_rounds(id,pack_id,number,type) SELECT $2,id,1,'artist_by_logo' FROM p RETURNING id)
		INSERT INTO quiz_questions(id,round_id,number,artist_id,primary_text,answer,status)
		SELECT $3::uuid,r.id,1,a.id,'Назовите исполнителя',a.name,'ready'
		FROM r CROSS JOIN artists a WHERE a.name='Mötley Crüe'
		UNION ALL
		SELECT $4::uuid,r.id,2,a.id,'Назовите исполнителя',a.name,'ready'
		FROM r CROSS JOIN artists a WHERE a.name='Mötley Light'`, ids.pack, ids.round, ids.first, ids.second); err != nil {
		t.Fatal(err)
	}

	return ids
}
