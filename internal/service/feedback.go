package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"devkeeper.com/quiz/internal/domain"
	"devkeeper.com/quiz/internal/store"
)

// FeedbackForm содержит тип и текст жалобы игрока.
type FeedbackForm struct {
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

// AddQuestionReport проверяет и сохраняет жалобу пользователя.
func (a *App) AddQuestionReport(ctx context.Context, question, user uuid.UUID, f FeedbackForm) error {
	switch f.Type {
	case "question_error", "bad_audio", "bug":
	default:
		return fmt.Errorf("%w: неизвестный тип жалобы", domain.ErrInvalid)
	}
	f.Comment = strings.TrimSpace(f.Comment)
	if !utf8.ValidString(f.Comment) || utf8.RuneCountInString(f.Comment) < 1 || utf8.RuneCountInString(f.Comment) > 4000 {
		return fmt.Errorf("%w: комментарий должен содержать от 1 до 4000 символов", domain.ErrInvalid)
	}
	return a.uow.AddQuestionReport(ctx, question, user, f.Type, f.Comment)
}

// QuestionLiked возвращает состояние лайка пользователя.
func (a *App) QuestionLiked(ctx context.Context, question, user uuid.UUID) (bool, error) {
	return a.uow.QuestionLiked(ctx, question, user)
}

// SetQuestionLike устанавливает лайк пользователя.
func (a *App) SetQuestionLike(ctx context.Context, question, user uuid.UUID, liked bool) error {
	return a.uow.SetQuestionLike(ctx, question, user, liked)
}

// FeedbackReports возвращает отдельные жалобы.
func (a *App) FeedbackReports(ctx context.Context, p domain.Page) ([]store.FeedbackReport, int64, error) {
	return a.uow.FeedbackReports(ctx, p)
}

// FeedbackLikes возвращает понравившиеся вопросы со счётчиками.
func (a *App) FeedbackLikes(ctx context.Context, p domain.Page) ([]store.FeedbackLike, int64, error) {
	return a.uow.FeedbackLikes(ctx, p)
}

// FeedbackReport загружает жалобу и снимок вопроса.
func (a *App) FeedbackReport(ctx context.Context, id uuid.UUID) (store.FeedbackReport, error) {
	return a.uow.FeedbackReport(ctx, id, false)
}

// DeleteReport удаляет одну жалобу.
func (a *App) DeleteReport(ctx context.Context, id uuid.UUID) error {
	return a.uow.DeleteReport(ctx, id)
}

// ReportActionForm содержит параметры изменения каталога по жалобе.
type ReportActionForm struct {
	Tags       *[]string `json:"tags"`
	Action     string    `json:"-"`
	YTMusicURL string    `json:"yt_music_url"`
	ArtistID   uuid.UUID `json:"artist_id"`
	AlbumID    uuid.UUID `json:"album_id"`
}

// ResolveReport выполняет действие над объектом из жалобы и удаляет её в одной транзакции.
func (a *App) ResolveReport(ctx context.Context, id uuid.UUID, f ReportActionForm) error {
	if f.Action == "reset-artist" {
		f.YTMusicURL = strings.TrimSpace(f.YTMusicURL)
		if err := validateYTArtistURL(f.YTMusicURL); err != nil {
			return err
		}
	}

	return a.uow.Transaction(ctx, func(tx *store.UnitOfWork) error {
		report, err := tx.FeedbackReport(ctx, id, true)
		if err != nil {
			return err
		}

		if report.QuestionDeleted {
			return fmt.Errorf("%w: вопрос уже снят с игры", domain.ErrConflict)
		}

		if err := applyReportAction(ctx, tx, report, f); err != nil {
			return err
		}

		return tx.DeleteReport(ctx, id)
	})
}

func applyReportAction(
	ctx context.Context, tx *store.UnitOfWork, report store.FeedbackReport, f ReportActionForm,
) error {
	q := report.Question

	switch f.Action {
	case "delete-artist", "reset-artist":
		if q.ArtistID == nil {
			break
		}

		if f.Action == "reset-artist" {
			return tx.Artists.ResetSource(ctx, *q.ArtistID, f.YTMusicURL)
		}

		return tx.Artists.Delete(ctx, *q.ArtistID)
	case "edit-artist-tags":
		if q.ArtistID == nil {
			break
		}

		if f.Tags == nil {
			return fmt.Errorf("%w: укажите список тегов", domain.ErrInvalid)
		}

		return editReportArtistTags(ctx, tx, *q.ArtistID, *f.Tags)
	case "delete-track":
		if q.TrackID != nil {
			return tx.Tracks.Delete(ctx, *q.TrackID)
		}
	case "reject-cover":
		if q.RoundType != "artist_by_cover" || !q.HasImage || len(q.Albums) != 1 {
			break
		}

		media, err := tx.FeedbackMedia(ctx, report.ID)
		if err != nil {
			return err
		}

		if media.ImageDownloadID == nil {
			break
		}

		err = tx.Albums.RejectQuestionCover(ctx, q.Albums[0].ID, *media.ImageDownloadID)
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("%w: версия обложки уже изменилась", domain.ErrConflict)
		}

		return err
	case "add-track-coauthor", "add-album-coauthor":
		if f.ArtistID == uuid.Nil || q.ArtistID == nil || f.ArtistID == *q.ArtistID {
			return fmt.Errorf("%w: выберите другого исполнителя из каталога", domain.ErrInvalid)
		}

		if f.Action == "add-track-coauthor" && q.TrackID != nil {
			return tx.Tracks.AddCoartist(ctx, *q.TrackID, f.ArtistID)
		}

		if f.Action == "add-album-coauthor" && slices.ContainsFunc(q.Albums, func(album store.FeedbackAlbum) bool {
			return album.ID == f.AlbumID
		}) {
			return tx.Albums.AddCoartist(ctx, f.AlbumID, f.ArtistID)
		}
	}

	return fmt.Errorf("%w: действие недоступно для этого вопроса", domain.ErrInvalid)
}

func editReportArtistTags(ctx context.Context, tx *store.UnitOfWork, artistID uuid.UUID, tags []string) error {
	err := tx.Artists.SetTags(ctx, artistID, tags)
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("%w: исполнитель или справочник тегов изменился", domain.ErrConflict)
	}

	return err
}

// ReportImage возвращает путь к оригиналу или PNG с игровыми масками.
func (a *App) ReportImage(ctx context.Context, id uuid.UUID, answer bool) (string, []byte, error) {
	media, err := a.uow.FeedbackMedia(ctx, id)
	if err != nil {
		return "", nil, err
	}

	imageID := media.ImageDownloadID

	if answer {
		imageID = media.AnswerImageDownloadID
	}

	if imageID == nil {
		return "", nil, domain.ErrNotFound
	}

	if !answer && len(media.CoverRectangles) > 0 {
		data, err := a.coverPNG(ctx, *imageID, media.CoverRectangles)
		return "", data, err
	}

	path, err := a.DownloadPath(ctx, *imageID)

	return path, nil, err
}

// ReportAudio возвращает путь к тому же отрывку, что звучал в вопросе.
func (a *App) ReportAudio(ctx context.Context, id uuid.UUID) (string, error) {
	media, err := a.uow.FeedbackMedia(ctx, id)
	if err != nil {
		return "", err
	}

	if media.ClipName == "" {
		return "", domain.ErrNotFound
	}

	return a.clipPath(ctx, media.ClipName)
}
