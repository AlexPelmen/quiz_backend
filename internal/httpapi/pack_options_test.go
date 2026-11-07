package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestPackRoundOptions(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpapi.Router(service.New(uow, logger, ""), logger)
	call := client(t, router)

	for i := range 10 {
		seedHTTPTrack(t, pool, i)

		name := fmt.Sprintf("%c%s%c", 'a'+i, strings.Repeat("m", i), 'b'+i)
		if i == 0 {
			name = "Аб"
		}

		if _, err := pool.Exec(t.Context(), "UPDATE artists SET name=$1 WHERE name=$2",
			name, fmt.Sprintf("Исполнитель %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO albums(id,name,artist_id,points)
		SELECT gen_random_uuid(),'Релиз ' || n,id,n FROM artists CROSS JOIN generate_series(1,12) AS n`); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"artist_cities", "artist_growth", "artist_alphabet", "artist_by_albums"} {
		t.Run(kind, func(t *testing.T) {
			post := client(t, router)
			body := fmt.Sprintf(`{"name":"Раунд","difficulty":"hard","round_types":[%q]}`, kind)

			var pack domain.QuizPack

			decodeResult(t, post("POST", "/api/v1/quiz-packs", body, 201), &pack)
			checkSelectedRound(t, pack, kind)
		})
	}

	call("POST", "/api/v1/quiz-packs", `{"name":"Пустой","round_types":[]}`, 400)
	call("POST", "/api/v1/quiz-packs", `{"name":"Повтор","round_types":["artist_cities","artist_cities"]}`, 400)

	storage, err := json.Marshal(map[string]string{"storage_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	call("PATCH", "/api/v1/settings", string(storage), 200)
	testPackCover(t, router)
}

func checkSelectedRound(t *testing.T, pack domain.QuizPack, kind string) {
	t.Helper()

	if pack.Difficulty != "hard" || len(pack.Rounds) != 1 || pack.Rounds[0].Type != kind ||
		len(pack.Rounds[0].Questions) != 10 {
		t.Fatal("пакет не соответствует выбранным параметрам")
	}

	for i, q := range pack.Rounds[0].Questions {
		if kind == "artist_by_albums" {
			want := []string{"Альбом"}
			for n := 12; n >= 4; n-- {
				want = append(want, fmt.Sprintf("Релиз %d", n))
			}

			if q.SecondaryText != strings.Join(want, "\n") || q.SoundDownloadID != nil {
				t.Fatalf("неверная подсказка по альбомам: %q", q.SecondaryText)
			}

			continue
		}

		if (kind == "artist_cities" || kind == "artist_alphabet") && q.SecondaryText != "" {
			t.Fatalf("лишняя подсказка в раунде %s: %q", kind, q.SecondaryText)
		}

		name := domain.TransliterateRussian(strings.ToLower(q.Answer))
		if name[0] != byte('a'+i) || name[len(name)-1] != byte('b'+i) || len(name) != 2+i || q.SoundDownloadID == nil {
			t.Fatalf("неверный вопрос в последовательности на позиции %d: %q", i, q.Answer)
		}
	}
}

func testPackCover(t *testing.T, router http.Handler) {
	t.Helper()

	image, err := os.ReadFile(makeCoverFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer

	form := multipart.NewWriter(&body)
	data := `{"name":"С картинкой","difficulty":"easy","round_types":["artist_by_track"]}`

	if err := form.WriteField("pack", data); err != nil {
		t.Fatal(err)
	}

	cover, err := form.CreateFormFile("cover", "cover.png")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cover.Write(image); err != nil {
		t.Fatal(err)
	}

	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/quiz-packs", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != 201 {
		t.Fatalf("пакет с обложкой: %d %s", response.Code, response.Body)
	}

	var pack domain.QuizPack

	decodeResult(t, response.Body.Bytes(), &pack)

	if pack.Difficulty != "easy" || pack.CoverDownloadID == nil {
		t.Fatal("обложка или сложность не сохранены")
	}

	got := client(t, router)("GET", "/api/v1/downloads/"+pack.CoverDownloadID.String()+"/file", "", 200)
	if !bytes.Equal(got, image) {
		t.Fatal("загруженная обложка отличается от исходной")
	}
}
