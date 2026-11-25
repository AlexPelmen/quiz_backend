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

func (a *api) feedbackReports(c *gin.Context) {
	p, err := page(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	rows, total, err := a.app.FeedbackReports(c.Request.Context(), p)
	a.respond(c, gin.H{"items": rows, "total": total}, err)
}

func (a *api) feedbackLikes(c *gin.Context) {
	p, err := page(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	rows, total, err := a.app.FeedbackLikes(c.Request.Context(), p)
	a.respond(c, gin.H{"items": rows, "total": total}, err)
}

func (a *api) feedbackReport(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	report, err := a.app.FeedbackReport(c.Request.Context(), *id)
	a.respond(c, report, err)
}

func (a *api) deleteFeedbackReport(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	if err = a.app.DeleteReport(c.Request.Context(), *id); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *api) resolveFeedbackReport(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var form service.ReportActionForm
	if err = decode(c, &form); err != nil {
		a.respond(c, nil, err)
		return
	}

	form.Action = c.Param("action")

	if err := a.app.ResolveReport(c.Request.Context(), *id, form); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *api) feedbackImage(answer bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := requestID(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}

		path, data, err := a.app.ReportImage(c.Request.Context(), *id, answer)
		if data != nil {
			a.coverResponse(c, data, err)
			return
		}

		c.Header("Cache-Control", "no-store")
		a.serveFile(c, path, err)
	}
}

func (a *api) feedbackAudio(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Header("Cache-Control", "no-store")

	path, err := a.app.ReportAudio(c.Request.Context(), *id)
	if err == nil {
		c.Header("Content-Type", "audio/mpeg")
		c.Header("Content-Disposition", "inline")
	}

	a.serveFile(c, path, err)
}
