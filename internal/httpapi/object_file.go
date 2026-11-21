package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/gin-gonic/gin"

	"devkeeper.com/quiz/internal/objectstorage"
)

func (a *api) serveObject(c *gin.Context, uri string) {
	ctx := c.Request.Context()

	obj, err := a.app.OpenObject(ctx, uri, c.Request.Header)
	if err != nil {
		c.Writer.Header().Del("Content-Disposition")
		c.Writer.Header().Del("Content-Type")

		switch code := objectstorage.Status(err); code {
		case http.StatusNotModified, http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable:
			c.Status(code)
		default:
			a.respond(c, nil, err)
		}

		return
	}

	headers := c.Writer.Header()
	if headers.Get("Content-Type") == "" {
		contentType := aws.ToString(obj.ContentType)
		if contentType == "" || contentType == "application/octet-stream" {
			contentType = objectstorage.ContentType(uri)
		}

		headers.Set("Content-Type", contentType)
	}

	headers.Set("Accept-Ranges", "bytes")

	if obj.ContentLength != nil {
		headers.Set("Content-Length", strconv.FormatInt(*obj.ContentLength, 10))
	}

	if obj.ETag != nil {
		headers.Set("ETag", *obj.ETag)
	}

	if obj.LastModified != nil {
		headers.Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	}

	status := http.StatusOK

	if obj.ContentRange != nil {
		headers.Set("Content-Range", *obj.ContentRange)

		status = http.StatusPartialContent
	}

	c.Status(status)
	c.Writer.WriteHeaderNow()

	_, err = io.Copy(c.Writer, obj.Body)
	if err := errors.Join(err, obj.Body.Close()); err != nil && ctx.Err() == nil {
		a.log.WarnContext(ctx, "поток S3 прерван", "path", c.Request.URL.Path, "error", err)
	}
}
