package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (a *api) listTags(c *gin.Context) {
	tags, err := a.app.Tags(c.Request.Context())
	a.respond(c, tags, err)
}

func (a *api) saveTag(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	var form struct {
		Name string `json:"name"`
	}

	if err := decode(c, &form); err != nil {
		a.respond(c, nil, err)
		return
	}

	tag, err := a.app.SaveTag(c.Request.Context(), id, form.Name)
	a.respond(c, tag, err)
}

func (a *api) deleteTag(c *gin.Context) {
	id, err := requestID(c)
	if err != nil {
		a.respond(c, nil, err)
		return
	}

	if err := a.app.DeleteTag(c.Request.Context(), *id); err != nil {
		a.respond(c, nil, err)
		return
	}

	c.Status(http.StatusNoContent)
}
