package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/service"
)

func (a *api) generatePackWithCover(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 101<<20)

	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}

	defer func() {
		if err := c.Request.MultipartForm.RemoveAll(); err != nil {
			a.log.Error("удаление временных файлов пакета", "error", err)
		}
	}()

	var f service.PackForm

	data := c.Request.FormValue("pack")
	if len(data) > 1<<20 || decodeObject(data, &f) != nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}

	cover, _, err := c.Request.FormFile("cover")
	if err != nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}

	defer func() {
		if err := cover.Close(); err != nil {
			a.log.Error("закрытие обложки пакета", "error", err)
		}
	}()

	value, err := a.app.Generate(c.Request.Context(), f, cover)
	a.respond(c, value, err)
}
