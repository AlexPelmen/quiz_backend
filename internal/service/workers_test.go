package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/source"
	"devkeeper.com/quiz/internal/testutil"
)

type catalogFixture struct{}

type failingCatalog struct{}

type timeoutCatalog struct{ calls int }

func (c *timeoutCatalog) Catalog(context.Context, source.Lookup) (source.Catalog, error) {
	c.calls++
	return source.Catalog{}, context.DeadlineExceeded
}

func TestParsingTimeoutRetries(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO artists(id,name) VALUES ($1,'Исполнитель')`, id); err != nil {
		t.Fatal(err)
	}
	run, err := uow.Runs.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err = uow.Runs.Claim(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	artist, err := uow.Artists.Claim(ctx, 60, run.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	src := &timeoutCatalog{}
	err = app.parseArtistWithRetry(ctx, &run, artist, parseDependencies{Settings: domain.DefaultSettings(), Source: src}, 0)
	if !errors.Is(err, context.DeadlineExceeded) || src.calls != 4 {
		t.Fatalf("ожидались три повтора: попыток %d, ошибка %v", src.calls, err)
	}
	if _, err := uow.Runs.Stop(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.waitParseRetry(ctx, &run, artist.ID, domain.DefaultSettings(), time.Minute); !errors.Is(err, errParsingStopped) {
		t.Fatalf("ожидалась остановка во время паузы: %v", err)
	}
}

type stoppingCatalog struct {
	stop func() error
}

func (c stoppingCatalog) Catalog(context.Context, source.Lookup) (source.Catalog, error) {
	if err := c.stop(); err != nil {
		return source.Catalog{}, err
	}
	return source.Catalog{}, errors.New("ошибка текущего исполнителя")
}

func TestStopParsingAfterCurrentArtist(t *testing.T) {
	pool, uow := testutil.Database(t)
	ctx := t.Context()
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	_, err := pool.Exec(ctx, `INSERT INTO artists(id,name) VALUES ($1,'Первый'),($2,'Второй')`, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	run, err := uow.Runs.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err = uow.Runs.Claim(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	deps := parseDependencies{Settings: domain.DefaultSettings(), Source: stoppingCatalog{stop: func() error {
		_, err := uow.Runs.Stop(ctx, run.ID)
		return err
	}}}
	if err := app.processRun(ctx, &run, deps); err != nil {
		t.Fatal(err)
	}
	run.Status = "finished"
	if err := uow.Runs.Finish(ctx, run); err != nil {
		t.Fatal(err)
	}
	saved, err := uow.Runs.Get(ctx, run.ID)
	if err != nil || saved.Status != "stopped" || saved.Processed != 1 || saved.Failed != 1 {
		t.Fatalf("неверный результат остановки: %+v, %v", saved, err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artists WHERE status='created'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("следующий исполнитель не остался в очереди: %d, %v", remaining, err)
	}
}

func (failingCatalog) Catalog(context.Context, source.Lookup) (source.Catalog, error) {
	return source.Catalog{}, errors.New("источник не нашёл исполнителя")
}

func TestFailedArtistsRetriedOncePerRun(t *testing.T) {
	pool, uow := testutil.Database(t)
	app := New(uow, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	ctx := t.Context()
	errArtist, manual := uuid.New(), uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO artists(id,name,status,updated_at) VALUES
		($1,'Ошибка','errored',now()-interval '1 hour'),($2,'Вручную','manual',now()-interval '1 hour')`, errArtist, manual)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		run, err := uow.Runs.Create(ctx)
		if err != nil {
			t.Fatal(err)
		}

		limited, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = app.processRun(limited, &run, parseDependencies{Settings: domain.DefaultSettings(), Source: failingCatalog{}})

		cancel()

		if err != nil || run.Processed != 1 || run.Failed != 1 {
			t.Fatalf("исполнитель не обработан ровно один раз: %+v, %v", run, err)
		}

		run.Status = "finished"
		if err := uow.Runs.Finish(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	got, err := uow.Artists.Get(ctx, manual)
	if err != nil || got.Status != "manual" {
		t.Fatalf("ручной исполнитель попал в очередь: %+v, %v", got, err)
	}
}

func (catalogFixture) Catalog(context.Context, source.Lookup) (source.Catalog, error) {
	return source.Catalog{
		ArtistID: 1, Name: "Группа",
		Albums: []source.Album{{ID: 2, Name: "Альбом", TrackList: "position,title\n1,Трек\n2,Другой\n", Complete: true}},
		Tracks: []source.Song{{ID: 3, Title: "Трек", Album: &source.Album{ID: 2}}},
	}, nil
}

func TestParserPersistsUnavailableTrack(t *testing.T) {
	_, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := New(uow, logger, "test")

	artist, err := uow.Artists.Create(t.Context(), domain.Artist{
		ID: uuid.New(), Name: "Группа",
		Points: 65, Tags: []string{"рок"},
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, `{"artists":[]}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	s := domain.DefaultSettings()
	s.MusicBrainzURL = server.URL

	deps := parseDependencies{Settings: s, Source: catalogFixture{}, Enrichment: source.NewMusicBrainz(s)}
	for range 2 {
		if err := app.parseArtist(t.Context(), artist, deps); err != nil {
			t.Fatal(err)
		}
	}

	tracks, err := uow.Tracks.List(t.Context(), domain.Page{Limit: 50})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("трек без источника потерян или дублирован: %+v, %v", tracks, err)
	}

	albums, err := uow.Albums.List(t.Context(), domain.Page{Limit: 50})
	if err != nil || len(albums) != 1 || !albums[0].TrackListComplete {
		t.Fatalf("полный треклист потерян: %+v, %v", albums, err)
	}

	if err := app.downloadNext(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	downloads, err := uow.Downloads.List(t.Context(), domain.Page{Limit: 50})
	if err != nil || len(downloads) != 1 || downloads[0].Status != "manual" {
		t.Fatalf("не создана задача manual: %+v, %v", downloads, err)
	}

	saved, err := uow.Artists.Get(t.Context(), artist.ID)
	if err != nil || saved.Points != 65 || saved.Status != "parsed" || len(saved.Tags) != 1 {
		t.Fatalf("пользовательские поля потеряны: %+v, %v", saved, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() { app.RunWorkers(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("фоновые горутины не завершились")
	}

	if _, err := app.Generate(t.Context(), PackForm{Name: "Без звука"}, nil); !errors.Is(err, domain.ErrInsufficient) {
		t.Fatalf("вопрос без доступного звука попал в пакет: %v", err)
	}
}
