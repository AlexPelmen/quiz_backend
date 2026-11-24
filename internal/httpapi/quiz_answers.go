package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/service"
)

func (a *api) answerOptions(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	options, err := a.app.FindAnswerOptions(ctx, c.Query("q"), c.Query("prefer"))
	a.respond(c, options, err)
}

func (a *api) submitAnswer(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := auth.UserID(ctx)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "требуется вход"})
		return
	}

	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var f service.AnswerForm
	if err := decode(c, &f); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Header("Cache-Control", "no-store")

	result, err := a.app.SubmitQuestionAnswer(ctx, *id, userID, f)
	a.respond(c, result, err)
}
