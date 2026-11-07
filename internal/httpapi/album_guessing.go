package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/service"
)

func (a *api) guessingList(c *gin.Context) {
	p, err := page(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	shuffle := c.Query("shuffle")
	if shuffle != "" {
		if _, err := strconv.ParseUint(shuffle, 10, 32); err != nil {
			a.respond(c, nil, domain.ErrInvalid)
			return
		}
	}

	value, err := a.app.GuessingList(c.Request.Context(), p, shuffle)
	a.respond(c, value, err)
}

func (a *api) saveGuessing(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var f service.GuessingForm

	if err := decode(c, &f); err != nil {
		a.respond(c, nil, err)
		return
	}

	if err := a.app.SaveGuessing(c.Request.Context(), *id, f); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *api) guessingPreview(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	coverID, err := uuid.Parse(c.Query("cover_id"))
	if err != nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}

	data, err := a.app.GuessingPreview(c.Request.Context(), *id, coverID)
	a.coverResponse(c, data, err)
}

func (a *api) questionCover(c *gin.Context) {
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

	data, err := a.app.QuestionCover(c.Request.Context(), *id, number)
	a.coverResponse(c, data, err)
}

func (a *api) coverResponse(c *gin.Context, data []byte, err error) {
	c.Header("Cache-Control", "no-store")

	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Data(http.StatusOK, "image/png", data)
}
