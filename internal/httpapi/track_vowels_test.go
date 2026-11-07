package httpapi_test

import (
	"io"
	"log/slog"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestTrackVowelsRound(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	for i := range 12 {
		seedHTTPTrack(t, pool, i)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE tracks t SET name = v.title FROM artists a JOIN (VALUES
		('Исполнитель 0','АУ (Live (2020))'), ('Исполнитель 1','ЪЬ'), ('Исполнитель 2','AEIOUY')
	) AS v(artist,title) ON a.name=v.artist WHERE a.id=t.artist_id`); err != nil {
		t.Fatal(err)
	}

	call("PATCH", "/api/v1/settings", `{"round_types":["track_vowels","track_vowels","track_vowels","track_vowels",
		"track_vowels","track_vowels","track_vowels","track_vowels","track_vowels","track_vowels"]}`, 200)
	call("POST", "/api/v1/quiz-packs", `{"name":"Девять подходящих исполнителей"}`, 422)

	// У десятого исполнителя есть и подходящий трек, и трек без согласных с гораздо большим весом.
	if _, err := pool.Exec(t.Context(), `WITH updated AS (
		UPDATE tracks SET name='ЁЛКА — HELLO!', points=1e-9 WHERE name='АУ (Live (2020))' RETURNING id,artist_id
	), extra AS (
		INSERT INTO tracks(id,name,artist_id) SELECT gen_random_uuid(),'АУ',artist_id FROM updated RETURNING id
	) INSERT INTO track_to_download(track_id,download_id)
		SELECT extra.id,td.download_id FROM extra,updated JOIN track_to_download td ON td.track_id=updated.id`); err != nil {
		t.Fatal(err)
	}

	var pack domain.QuizPack

	decodeResult(t, call("POST", "/api/v1/quiz-packs", `{"name":"Десять исполнителей"}`, 201), &pack)

	if len(pack.Rounds) != 1 || len(pack.Rounds[0].Questions) != 10 {
		t.Fatal("ожидался полный раунд по гласным")
	}

	for _, q := range pack.Rounds[0].Questions {
		if q.Answer == "ЁЛКА — HELLO!" && q.SecondaryText == "ёа — eo!" {
			return
		}
	}

	t.Fatal("не найден подходящий трек со строчной подсказкой без согласных")
}
