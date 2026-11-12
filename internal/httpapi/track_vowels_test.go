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
	) AS v(artist,title) ON a.name=v.artist
    JOIN artist_to_track owner ON owner.artist_id=a.id WHERE owner.track_id=t.id`); err != nil {
		t.Fatal(err)
	}

	call("PATCH", "/api/v1/settings", `{"round_types":["track_vowels","track_vowels","track_vowels","track_vowels",
		"track_vowels","track_vowels","track_vowels","track_vowels","track_vowels","track_vowels"]}`, 200)
	call("POST", "/api/v1/quiz-packs", `{"name":"Девять подходящих исполнителей","question_count":10,"round_types":["track_vowels"]}`, 422)

	// У десятого исполнителя есть и подходящий трек, и трек без согласных с гораздо большим весом.
	if _, err := pool.Exec(t.Context(), `WITH updated AS (
		UPDATE tracks SET name='ЁЛКА — HELLO!', yt_plays=1 WHERE name='АУ (Live (2020))' RETURNING id
	), extra AS (
		INSERT INTO tracks(id,name,yt_plays) SELECT gen_random_uuid(),'АУ',100000 FROM updated RETURNING id
	), linked AS (
        INSERT INTO artist_to_track(track_id,artist_id)
        SELECT extra.id,owner.artist_id FROM extra,updated JOIN artist_to_track owner ON owner.track_id=updated.id
        RETURNING track_id
    ) INSERT INTO track_to_download(track_id,download_id)
		SELECT linked.track_id,td.download_id
            FROM linked,updated JOIN track_to_download td ON td.track_id=updated.id`); err != nil {
		t.Fatal(err)
	}

	var pack domain.QuizPack

	decodeResult(t, call("POST", "/api/v1/quiz-packs", `{"name":"Десять исполнителей","question_count":10,"round_types":["track_vowels"]}`, 201), &pack)

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
