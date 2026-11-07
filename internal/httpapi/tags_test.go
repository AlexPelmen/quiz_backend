package httpapi_test

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/httpapi"
	"devkeeper.com/quiz/internal/service"
	"devkeeper.com/quiz/internal/store"
	"devkeeper.com/quiz/internal/testutil"
)

func TestTagsWorkflow(t *testing.T) {
	_, uow := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := client(t, httpapi.Router(service.New(uow, logger, ""), logger))

	var (
		artist  domain.Artist
		track   domain.Track
		album   domain.Album
		tags    []store.Tag
		free    store.Tag
		artists []struct {
			Tags []string `json:"tags"`
		}
	)

	decodeResult(t, call("POST", "/api/v1/artists", `{"name":"Artist","tags":["rock"]}`, 201), &artist)

	trackBody := fmt.Sprintf(`{"name":"Track","artist_id":%q,"tags":["rock"]}`, artist.ID)
	albumBody := fmt.Sprintf(`{"name":"Album","artist_id":%q,"tags":["rock"]}`, artist.ID)

	decodeResult(t, call("POST", "/api/v1/tracks", trackBody, 201), &track)
	decodeResult(t, call("POST", "/api/v1/albums", albumBody, 201), &album)
	decodeResult(t, call("GET", "/api/v1/tags", "", 200), &tags)

	if len(tags) != 1 || tags[0].Name != "rock" {
		t.Fatalf("unexpected tags: %+v", tags)
	}

	url := "/api/v1/tags/" + tags[0].ID.String()
	call("DELETE", url, "", 409)
	call("PATCH", url, `{"name":"alternative"}`, 200)
	decodeResult(t, call("GET", "/api/v1/tracks/"+track.ID.String(), "", 200), &track)
	decodeResult(t, call("GET", "/api/v1/albums/"+album.ID.String(), "", 200), &album)

	if len(track.Tags) != 1 || track.Tags[0] != "alternative" || len(album.Tags) != 1 || album.Tags[0] != "alternative" {
		t.Fatalf("catalog tags not renamed: track=%v album=%v", track.Tags, album.Tags)
	}

	decodeResult(t, call("GET", "/api/v1/artists?tag=alternative", "", 200), &artists)

	if len(artists) != 1 || len(artists[0].Tags) != 1 || artists[0].Tags[0] != "alternative" {
		t.Fatalf("tag rename not propagated: %+v", artists)
	}

	call("POST", "/api/v1/tags", `{"name":"alternative"}`, 409)
	call("POST", "/api/v1/tags", `{"name":"  "}`, 400)
	decodeResult(t, call("POST", "/api/v1/tags", `{"name":" jazz "}`, 201), &free)

	if free.Name != "jazz" {
		t.Fatalf("unexpected name: %q", free.Name)
	}

	call("DELETE", "/api/v1/tags/"+free.ID.String(), "", 204)
}
