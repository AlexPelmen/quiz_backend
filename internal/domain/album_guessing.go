package domain

import "github.com/google/uuid"

// CoverRectangle хранит два угла маски в долях 1/10000 от размера обложки.
type CoverRectangle struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

// AlbumGuessing — обложка в очереди модерации и её текущие маски.
type AlbumGuessing struct {
	AlbumID         uuid.UUID        `json:"album_id"`
	AlbumName       string           `json:"album_name"`
	ArtistName      string           `json:"artist_name"`
	CoverDownloadID uuid.UUID        `json:"cover_download_id"`
	Status          string           `json:"status"`
	Rectangles      []CoverRectangle `json:"rectangles"`
}
