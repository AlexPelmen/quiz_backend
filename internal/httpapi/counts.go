package httpapi

import "github.com/gin-gonic/gin"

func (a *api) catalogCount(resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := page(c)
		if err != nil {
			a.respond(c, nil, err)
			return
		}
		p.Name = c.Query("name")
		count, err := a.app.ListCount(c.Request.Context(), resource, p)
		a.respond(c, gin.H{"count": count}, err)
	}
}

func (a *api) downloadActiveCount(c *gin.Context) {
	kind := c.Query("file_type")
	if kind != "mp3" && kind != "image" {
		c.JSON(400, gin.H{"error": "неверный тип файла"})
		return
	}
	count, err := a.app.DownloadActiveCount(c.Request.Context(), kind)
	a.respond(c, gin.H{"active": count}, err)
}
