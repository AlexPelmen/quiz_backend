package httpapi_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/testutil"
)

func TestAlbumGuessingShuffle(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	for i := range 3 {
		seedHTTPTrack(t, pool, i)
	}

	if _, err := pool.Exec(t.Context(), `WITH next AS MATERIALIZED (
            SELECT gen_random_uuid() AS id,artist_id,cover_download_id FROM catalog_albums
        ),saved AS (
            INSERT INTO albums(id,name,cover_download_id)
            SELECT id,'Вторая обложка',cover_download_id FROM next RETURNING id
        ) INSERT INTO artist_to_album(album_id,artist_id)
            SELECT saved.id,next.artist_id FROM saved JOIN next USING(id)`); err != nil {
		t.Fatal(err)
	}

	const url = "/api/v1/album-guessing?status=pending&shuffle=4294967295"

	list := func(query string) []domain.AlbumGuessing {
		var rows []domain.AlbumGuessing

		decodeResult(t, call("GET", query, "", 200), &rows)

		return rows
	}
	all := list(url)
	first := list(url + "&limit=3")
	second := list(url + "&limit=3&offset=3")

	if len(first) != 3 || len(second) != 3 || len(all) != 6 {
		t.Fatalf("неожиданный размер очереди: всего %d, страницы %d и %d", len(all), len(first), len(second))
	}

	if !slices.EqualFunc(all, append(first, second...), func(a, b domain.AlbumGuessing) bool {
		return a.AlbumID == b.AlbumID
	}) {
		t.Fatal("порядок изменился между запросами страниц")
	}

	if first[0].ArtistName == first[1].ArtistName || first[0].ArtistName == first[2].ArtistName ||
		first[1].ArtistName == first[2].ArtistName {
		t.Fatal("первая страница должна содержать разных исполнителей")
	}

	call("GET", "/api/v1/album-guessing?shuffle=-1", "", 400)
}

func TestAlbumGuessing(t *testing.T) {
	pool, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	for i := range 10 {
		seedHTTPTrack(t, pool, i)
	}

	original := makeCoverFixture(t)
	if _, err := pool.Exec(t.Context(), "UPDATE downloads SET uri=$1 WHERE source_type='image'", original); err != nil {
		t.Fatal(err)
	}

	call("PATCH", "/api/v1/settings", `{"round_types":["artist_by_cover","artist_by_track","track_vowels",
		"artist_by_logo","artist_initial","artist_by_track","artist_by_track","artist_by_track",
		"artist_by_track","artist_by_track"]}`, 200)
	call("POST", "/api/v1/quiz-packs", `{"name":"Непроверенные обложки"}`, 422)

	var covers []domain.AlbumGuessing

	decodeResult(t, call("GET", "/api/v1/album-guessing?status=pending", "", 200), &covers)

	if len(covers) != 10 {
		t.Fatalf("в очереди %d обложек, ожидалось 10", len(covers))
	}

	mask := `[{"x1":0,"y1":0,"x2":2500,"y2":5000},{"x1":7500,"y1":5000,"x2":10000,"y2":10000}]`
	form := func(cover domain.AlbumGuessing, approved bool, rectangles string) string {
		return fmt.Sprintf(`{"cover_download_id":%q,"approved":%t,"rectangles":%s}`,
			cover.CoverDownloadID, approved, rectangles)
	}

	first := covers[0]
	albumURL := "/api/v1/albums/" + first.AlbumID.String()
	editURL := albumURL + "/guessing"
	previewURL := albumURL + "/guessing-preview?cover_id=" + first.CoverDownloadID.String()
	checkCoverPixels(t, call("GET", previewURL, "", 200), false)
	call("PUT", editURL, form(first, true, `[{"x1":5,"y1":0,"x2":1,"y2":10000}]`), 400)

	for _, cover := range covers {
		path := "/api/v1/albums/" + cover.AlbumID.String() + "/guessing"
		call("PUT", path, form(cover, true, mask), 204)
	}

	var pack domain.QuizPack

	raw := call("POST", "/api/v1/quiz-packs", `{"name":"Проверенные обложки"}`, 201)
	decodeResult(t, raw, &pack)

	if len(pack.Rounds) != 1 || len(pack.Rounds[0].Questions) != 10 {
		t.Fatal("не сформирован полный раунд по обложкам")
	}

	if bytes.Contains(raw, []byte(`"album_id"`)) || bytes.Contains(raw, []byte(first.CoverDownloadID.String())) {
		t.Fatal("в игровом пакете есть ссылка на исходную обложку")
	}

	var number int

	if err := pool.QueryRow(t.Context(), "SELECT number FROM quiz_questions WHERE album_id=$1", first.AlbumID).
		Scan(&number); err != nil {
		t.Fatal(err)
	}

	coverURL := fmt.Sprintf("/api/v1/quiz-packs/%s/questions/%d/cover", pack.ID, number)
	checkCoverPixels(t, call("GET", coverURL, "", 200), true)
	checkCoverPixels(t, call("GET", "/api/v1/downloads/"+first.CoverDownloadID.String()+"/file", "", 200), false)
	call("PUT", editURL, form(first, false, mask), 204)
	call("GET", coverURL, "", 404)
	call("POST", "/api/v1/quiz-packs", `{"name":"Недостаточно обложек"}`, 422)
	call("PUT", editURL, form(first, true, `[]`), 204)
	checkCoverPixels(t, call("GET", coverURL, "", 200), false)
	replaceCover(t, pool, first.AlbumID, original)
	call("GET", coverURL, "", 404)
	call("PUT", editURL, form(first, true, mask), 409)
}

func replaceCover(t *testing.T, pool *pgxpool.Pool, albumID uuid.UUID, original string) {
	t.Helper()

	replacement := uuid.New()

	if _, err := pool.Exec(t.Context(), `INSERT INTO downloads(id,source_type,status,file_type,uri)
		VALUES($1,'image','finished','png',$2)`, replacement, original); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), "UPDATE albums SET cover_download_id=$1 WHERE id=$2",
		replacement, albumID); err != nil {
		t.Fatal(err)
	}
}

func makeCoverFixture(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "cover.png")
	src := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	var data bytes.Buffer

	if err := png.Encode(&data, src); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func checkCoverPixels(t *testing.T, data []byte, masked bool) {
	t.Helper()

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	for y := range 4 {
		for x := range 8 {
			want := uint32(0xffff)
			if masked && ((x < 2 && y < 2) || (x >= 6 && y >= 2)) {
				want = 0
			}

			r, g, b, alpha := img.At(x, y).RGBA()
			if r != want || g != want || b != want || alpha != 0xffff {
				t.Fatalf("неверный цвет пикселя (%d,%d): %v", x, y, img.At(x, y))
			}
		}
	}
}
