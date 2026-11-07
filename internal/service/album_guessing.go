package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"

	"github.com/google/uuid"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

const coverScale = 10000

// GuessingForm привязывает решение и маски к просмотренной версии обложки.
type GuessingForm struct {
	CoverDownloadID uuid.UUID               `json:"cover_download_id"`
	Approved        *bool                   `json:"approved"`
	Rectangles      []domain.CoverRectangle `json:"rectangles"`
}

// GuessingList возвращает очередь модерации обложек.
func (a *App) GuessingList(ctx context.Context, page domain.Page, shuffle string) ([]domain.AlbumGuessing, error) {
	switch page.Status {
	case "", "pending", "approved", "rejected":
		return a.uow.Albums.GuessingList(ctx, page, shuffle)
	default:
		return nil, domain.ErrInvalid
	}
}

// SaveGuessing проверяет маски и записывает решение модератора.
func (a *App) SaveGuessing(ctx context.Context, albumID uuid.UUID, f GuessingForm) error {
	if f.CoverDownloadID == uuid.Nil || f.Approved == nil || len(f.Rectangles) > 32 {
		return domain.ErrInvalid
	}

	for _, r := range f.Rectangles {
		if r.X1 < 0 || r.Y1 < 0 || r.X2 > coverScale || r.Y2 > coverScale || r.X1 >= r.X2 || r.Y1 >= r.Y2 {
			return fmt.Errorf("%w: неверные координаты маски", domain.ErrInvalid)
		}
	}

	if *f.Approved {
		if _, err := a.readCover(ctx, f.CoverDownloadID); err != nil {
			return err
		}
	}

	if f.Rectangles == nil {
		f.Rectangles = []domain.CoverRectangle{}
	}

	err := a.uow.Albums.SaveGuessing(ctx, albumID, store.GuessingDecision{
		CoverID: f.CoverDownloadID, Approved: *f.Approved, Rectangles: f.Rectangles,
	})
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("%w: обложка изменилась, обновите очередь", domain.ErrConflict)
	}

	return err
}

// GuessingPreview возвращает обложку для редактора в тех же координатах, что и в игре.
func (a *App) GuessingPreview(ctx context.Context, albumID, coverID uuid.UUID) ([]byte, error) {
	album, err := a.uow.Albums.Get(ctx, albumID)
	if err != nil {
		return nil, err
	}

	if album.CoverDownloadID == nil || *album.CoverDownloadID != coverID {
		return nil, domain.ErrNotFound
	}

	return a.coverPNG(ctx, coverID, nil)
}

// QuestionCover заново рисует актуальные маски, не выдавая исходный файл.
func (a *App) QuestionCover(ctx context.Context, packID uuid.UUID, number int) ([]byte, error) {
	id, rectangles, err := a.uow.Packs.QuestionCover(ctx, packID, number)
	if err != nil {
		return nil, err
	}

	return a.coverPNG(ctx, id, rectangles)
}

func (a *App) coverPNG(ctx context.Context, id uuid.UUID, rectangles []domain.CoverRectangle) ([]byte, error) {
	src, err := a.readCover(ctx, id)
	if err != nil {
		return nil, err
	}

	bounds := src.Bounds()
	if len(rectangles) > 0 {
		out := image.NewNRGBA(bounds)
		draw.Draw(out, bounds, src, bounds.Min, draw.Src)

		for _, r := range rectangles {
			// Округляем наружу, чтобы край закрытой области не просвечивал.
			box := image.Rect(r.X1*bounds.Dx()/coverScale, r.Y1*bounds.Dy()/coverScale,
				(r.X2*bounds.Dx()+coverScale-1)/coverScale, (r.Y2*bounds.Dy()+coverScale-1)/coverScale)
			draw.Draw(out, box.Add(bounds.Min), image.NewUniform(color.Black), image.Point{}, draw.Src)
		}

		src = out
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var result bytes.Buffer

	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	err = encoder.Encode(&result, src)

	return result.Bytes(), err
}

func (a *App) readCover(ctx context.Context, id uuid.UUID) (image.Image, error) {
	path, err := a.DownloadPath(ctx, id)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err := file.Close(); err != nil {
			a.log.WarnContext(ctx, "не удалось закрыть файл обложки", "download_id", id, "error", err)
		}
	}()

	cfg, _, err := image.DecodeConfig(file)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
		return nil, fmt.Errorf("%w: нужна читаемая обложка PNG, JPEG, GIF, BMP или WebP до 4096×4096", domain.ErrInvalid)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("%w: не удалось прочитать обложку: %v", domain.ErrInvalid, err)
	}

	return src, nil
}
