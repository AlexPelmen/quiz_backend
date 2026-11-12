package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (a *api) systemLogs(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit должен быть от 1 до 1000"})
			return
		}
		limit = n
	}
	if a.logs == nil {
		c.JSON(http.StatusOK, gin.H{"entries": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": a.logs.Recent(limit)})
}
