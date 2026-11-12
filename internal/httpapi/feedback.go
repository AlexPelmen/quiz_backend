package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"devkeeper.com/quiz/internal/auth"
	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/service"
)

func (a *api) submitFeedback(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}
	user, ok := auth.UserID(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "требуется вход"})
		return
	}
	var form service.FeedbackForm
	if err = decode(c, &form); err != nil {
		a.respond(c, nil, err)
		return
	}
	if err = a.app.AddQuestionReport(c.Request.Context(), *id, user, form); err != nil {
		a.respond(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *api) questionLike(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}
	user, ok := auth.UserID(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "требуется вход"})
		return
	}
	if c.Request.Method == http.MethodGet {
		liked, err := a.app.QuestionLiked(c.Request.Context(), *id, user)
		a.respond(c, gin.H{"liked": liked}, err)
		return
	}
	var form struct {
		Liked *bool `json:"liked"`
	}
	if err = decode(c, &form); err != nil || form.Liked == nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}
	if err = a.app.SetQuestionLike(c.Request.Context(), *id, user, *form.Liked); err != nil {
		a.respond(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *api) feedbackQuestions(c *gin.Context) {
	p, err := page(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}
	rows, total, err := a.app.FeedbackQuestions(c.Request.Context(), c.Query("filter"), p)
	a.respond(c, gin.H{"items": rows, "total": total}, err)
}
func (a *api) questionReports(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}
	rows, err := a.app.QuestionReports(c.Request.Context(), *id)
	a.respond(c, rows, err)
}
func (a *api) viewFeedback(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}
	var form struct {
		Viewed *bool `json:"viewed"`
	}
	if err = decode(c, &form); err != nil || form.Viewed == nil {
		a.respond(c, nil, domain.ErrInvalid)
		return
	}
	if err = a.app.SetReportViewed(c.Request.Context(), *id, *form.Viewed); err != nil {
		a.respond(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}
