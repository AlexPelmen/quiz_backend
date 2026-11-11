package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

func mediaName(artist, title string, preview bool) string {
	clean := func(value string) string {
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
				return '_'
			}
			return r
		}, value)
		return strings.Trim(strings.TrimSpace(value), ". ")
	}
	artist, title = clean(artist), clean(title)
	if preview && title != "" {
		title = "превью " + title
	}
	name := artist
	if title != "" && artist != "" {
		name += " - " + title
	} else if name == "" {
		name = title
	}
	runes := []rune(name)
	if len(runes) > 120 {
		name = strings.TrimRight(string(runes[:120]), ". ")
	}
	upper := strings.ToUpper(name)
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		name = "_" + name
	}
	return name
}

// linkMediaFile создаёт имя без перезаписи файлов при параллельных загрузках.
func linkMediaFile(input, dir, name, extension string, id uuid.UUID) (string, error) {
	if name == "" {
		name = id.String()
	}
	path := filepath.Join(dir, name+"."+extension)
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		path = filepath.Join(dir, name+" - "+id.String()+"."+extension)
		out, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
	}
	in, err := os.Open(input)
	if err != nil {
		return "", errors.Join(err, out.Close(), os.Remove(path))
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, in.Close(), out.Close()); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	if err := os.Remove(input); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return path, nil
}
