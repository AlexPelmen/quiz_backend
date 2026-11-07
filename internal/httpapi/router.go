// Package httpapi предоставляет HTTP API музыкального квиз-сервиса.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/service"
)

type api struct {
	app *service.App
	log *slog.Logger
}

// Router создаёт маршруты API без авторизации.
func Router(app *service.App, logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.RecoveryWithWriter(os.Stderr))

	h := &api{app: app, log: logger}

	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/ready", h.ready)

	v1 := r.Group("/api/v1")
	for _, resource := range []string{"artists", "albums", "tracks", "downloads", "parsing-runs", "quiz-packs"} {
		v1.GET("/"+resource, h.read(resource))
		v1.GET("/"+resource+"/:id", h.read(resource))
	}

	for _, resource := range []string{"artists", "albums", "tracks", "quiz-packs"} {
		v1.POST("/"+resource, h.save(resource))
		v1.PATCH("/"+resource+"/:id", h.save(resource))
		v1.DELETE("/"+resource+"/:id", h.remove(resource))
	}

	v1.POST("/artists/:id/image", h.upload("artists"))
	v1.POST("/albums/:id/cover", h.upload("albums"))
	v1.POST("/tracks/:id/audio", h.upload("tracks"))
	v1.POST("/artists/:id/retry", h.retryArtist)
	v1.POST("/parsing-runs", h.startParsing)
	v1.POST("/downloads/:id/retry", h.retryDownload)
	v1.GET("/downloads/:id/file", h.file)
	v1.GET("/quiz-packs/:id/questions/:number/audio", h.audio)
	v1.POST("/quiz-rounds/:id/results", h.recordRoundResult)
	v1.GET("/settings", h.settings)
	v1.PATCH("/settings", h.patchSettings)
	v1.GET("/genius-quota", h.geniusQuota)
	v1.POST("/genius-quota/refresh", h.refreshGeniusQuota)

	return r
}

func (a *api) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := a.app.Ready(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (a *api) respond(c *gin.Context, value any, err error) {
	if err == nil {
		status := http.StatusOK
		if c.Request.Method == http.MethodPost {
			status = http.StatusCreated
		}

		c.JSON(status, value)

		return
	}

	code, message := http.StatusInternalServerError, "внутренняя ошибка сервиса"

	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, os.ErrNotExist):
		code, message = http.StatusNotFound, domain.ErrNotFound.Error()
	case errors.Is(err, domain.ErrInvalid):
		code, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, domain.ErrConflict):
		code, message = http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrInsufficient):
		code, message = http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		code, message = http.StatusGatewayTimeout, "превышено время ожидания"
	case errors.Is(err, context.Canceled):
		return
	}

	if code == http.StatusInternalServerError {
		a.log.Error("HTTP операция", "path", c.Request.URL.Path, "error", err)
	}

	c.JSON(code, gin.H{"error": message})
}

func requestID(c *gin.Context) (*uuid.UUID, error) {
	if c.Param("id") == "" {
		return nil, nil
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		return nil, domain.ErrInvalid
	}

	return &id, nil
}

func page(c *gin.Context) (domain.Page, error) {
	p := domain.Page{Limit: 50, Status: c.Query("status"), Tag: c.Query("tag")}
	for key, target := range map[string]*int{"limit": &p.Limit, "offset": &p.Offset} {
		if raw := c.Query(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return p, domain.ErrInvalid
			}

			*target = n
		}
	}

	if p.Limit < 1 || p.Limit > 200 {
		return p, domain.ErrInvalid
	}

	for key, target := range map[string]**uuid.UUID{
		"artist_id": &p.ArtistID, "album_id": &p.AlbumID, "track_id": &p.TrackID,
	} {
		if raw := c.Query(key); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				return p, domain.ErrInvalid
			}

			*target = &id
		}
	}

	return p, nil
}

func (a *api) read(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := requestID(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}

		p, err := page(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}

		if id == nil {
			p.Name = strings.TrimSpace(c.Query("name"))
			p.Sort, p.Order = c.Query("sort"), c.Query("order")

			if !validCatalogSort(resource, p) {
				a.respond(c, nil, fmt.Errorf("%w: недопустимая сортировка", domain.ErrInvalid))
				return
			}
		}

		value, err := a.app.Read(c.Request.Context(), service.ReadRequest{Resource: resource, ID: id, Page: p})
		a.respond(c, value, err)
	}
}

func validCatalogSort(resource string, p domain.Page) bool {
	if resource != "artists" && resource != "albums" && resource != "tracks" {
		return true
	}

	if p.Order != "" && p.Order != "asc" && p.Order != "desc" {
		return false
	}

	switch p.Sort {
	case "", "name", "points", "created_at":
		return true
	case "status":
		return resource == "artists" || resource == "tracks"
	case "release_date":
		return resource == "albums"
	default:
		return false
	}
}

func (a *api) upload(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := requestID(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}

		if c.ContentType() != "application/octet-stream" {
			c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Требуется application/octet-stream"})
			return
		}

		value, err := a.app.Upload(c.Request.Context(), resource, *id, c.Request.Body)
		a.respond(c, value, err)
	}
}

func body(c *gin.Context) ([]byte, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)

	data, err := io.ReadAll(c.Request.Body)
	if err != nil || !json.Valid(data) || !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		return nil, domain.ErrInvalid
	}

	return data, nil
}

func decode(c *gin.Context, target any) error {
	data, err := body(c)
	if err != nil {
		return err
	}

	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()

	if err := d.Decode(target); err != nil {
		return domain.ErrInvalid
	}

	return nil
}

func (a *api) save(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := requestID(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}

		switch resource {
		case "artists":
			var f service.ArtistForm
			if err := decode(c, &f); err != nil {
				a.respond(c, nil, err)
				return
			}

			value, err := a.app.SaveArtist(c.Request.Context(), id, f)
			a.respond(c, value, err)
		case "albums", "tracks":
			a.saveCatalog(c, resource, id)
		case "quiz-packs":
			a.savePack(c, id)
		}
	}
}

func (a *api) saveCatalog(c *gin.Context, resource string, id *uuid.UUID) {
	var f service.CatalogForm
	if err := decode(c, &f); err != nil {
		a.respond(c, nil, err)
		return
	}

	if resource == "albums" {
		value, err := a.app.SaveAlbum(c.Request.Context(), id, f)
		a.respond(c, value, err)
	} else {
		value, err := a.app.SaveTrack(c.Request.Context(), id, f)
		a.respond(c, value, err)
	}
}

func (a *api) savePack(c *gin.Context, id *uuid.UUID) {
	if id != nil {
		var f service.PackPatch
		if err := decode(c, &f); err != nil {
			a.respond(c, nil, err)
			return
		}

		value, err := a.app.PatchPack(c.Request.Context(), *id, f)
		a.respond(c, value, err)

		return
	}

	var f service.PackForm
	if err := decode(c, &f); err != nil {
		a.respond(c, nil, err)
		return
	}

	value, err := a.app.Generate(c.Request.Context(), f)
	a.respond(c, value, err)
}

func (a *api) recordRoundResult(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var f service.RoundResultForm
	if err := decode(c, &f); err != nil {
		a.respond(c, nil, err)
		return
	}

	if err := a.app.RecordRoundResult(c.Request.Context(), *id, f); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *api) remove(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := requestID(c)
		if err == nil {
			err = a.app.Delete(c.Request.Context(), resource, *id)
		}

		if err != nil {
			a.respond(c, nil, err)
			return
		}

		c.Status(http.StatusNoContent)
	}
}

func (a *api) startParsing(c *gin.Context) {
	value, err := a.app.StartParsing(c.Request.Context())
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.JSON(http.StatusAccepted, value)
}

func (a *api) retryArtist(c *gin.Context) {
	id, err := requestID(c)
	if err == nil {
		err = a.app.RetryArtist(c.Request.Context(), *id)
	}

	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *api) retryDownload(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var f service.DownloadForm
	if c.Request.ContentLength != 0 {
		if err := decode(c, &f); err != nil {
			a.respond(c, nil, err)
			return
		}
	}

	value, err := a.app.RetryDownload(c.Request.Context(), *id, f)
	a.respond(c, value, err)
}

func (a *api) settings(c *gin.Context) {
	value, err := a.app.Settings(c.Request.Context())
	a.respond(c, value, err)
}

func (a *api) geniusQuota(c *gin.Context) {
	value, err := a.app.GeniusQuota(c.Request.Context())
	a.respond(c, value, err)
}

func (a *api) refreshGeniusQuota(c *gin.Context) {
	value, err := a.app.RefreshGeniusQuota(c.Request.Context())
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.JSON(http.StatusOK, value)
}

func (a *api) patchSettings(c *gin.Context) {
	data, err := body(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	value, err := a.app.PatchSettings(c.Request.Context(), data)
	a.respond(c, value, err)
}

func (a *api) file(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	path, err := a.app.DownloadPath(c.Request.Context(), *id)
	a.serveFile(c, path, err)
}

func (a *api) audio(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	number, err := strconv.Atoi(c.Param("number"))
	if err != nil || number < 1 || number > 100 {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}

	path, err := a.app.Audio(c.Request.Context(), *id, number)
	if err == nil {
		_, err = os.Stat(path)
	}

	if err == nil {
		c.Header("Content-Type", "audio/mpeg")
		c.Header("Content-Disposition", "inline; filename=question-"+strconv.Itoa(number)+".mp3")
	}

	a.serveFile(c, path, err)
}

func (a *api) serveFile(c *gin.Context, path string, err error) {
	if err == nil {
		_, err = os.Stat(path)
	}

	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.File(path)
}
