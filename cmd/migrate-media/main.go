// Команда migrate-media переносит медиатеку Music Quiz в S3.
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"devkeeper.com/quiz/internal/objectstorage"
	"devkeeper.com/quiz/internal/store"
)

type options struct {
	mode, root, dbRoot string
	jobs               int
}

type mediaFile struct {
	key, filename string
	size          int64
}

type migration struct {
	options
	bucket *objectstorage.Bucket
	out    io.Writer
}

type migrationPlan struct {
	files   map[string]mediaFile
	mapping map[string]string
	count   int
}

func main() {
	var args options

	flag.StringVar(&args.mode, "mode", "plan", "probe, plan, copy или apply")
	flag.StringVar(&args.root, "root", "", "локальный каталог медиатеки, по умолчанию storage_dir")
	flag.StringVar(&args.dbRoot, "db-root", "", "старый префикс путей в БД, по умолчанию storage_dir")
	flag.IntVar(&args.jobs, "jobs", 8, "число одновременных проверок или загрузок (1–16)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args options, out io.Writer) error {
	if args.jobs < 1 || args.jobs > 16 {
		return errors.New("jobs должен быть от 1 до 16")
	}

	switch args.mode {
	case "probe", "plan", "copy", "apply":
	default:
		return errors.New("mode должен быть probe, plan, copy или apply")
	}

	bucket, err := objectstorage.FromEnv()
	if err != nil {
		return err
	}

	if args.mode == "probe" {
		if err := probeBucket(ctx, bucket); err != nil {
			return err
		}

		_, err := fmt.Fprintln(out, "Бакет S3: проверены список, запись, чтение с Range и удаление.")

		return err
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("требуется DATABASE_URL")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("некорректный DATABASE_URL")
	}

	defer pool.Close()

	uow := store.New(pool)
	m := migration{options: args, bucket: bucket, out: out}

	plan, err := m.plan(ctx, uow)
	if err != nil || args.mode == "plan" {
		return err
	}

	if err := bucket.Check(ctx); err != nil {
		return err
	}

	if err := m.transfer(ctx, plan.files); err != nil {
		return err
	}

	if args.mode == "apply" {
		if err := uow.ReplaceMediaLocations(ctx, plan.mapping, bucket.URI(""), plan.count); err != nil {
			return err
		}

		_, err := fmt.Fprintf(out, "Объекты проверены, ссылки в PostgreSQL обновлены: %d.\n", plan.count)

		return err
	}

	_, err = fmt.Fprintln(out, "Объекты скопированы и проверены. PostgreSQL не изменён.")

	return err
}

func (m migration) plan(ctx context.Context, uow *store.UnitOfWork) (migrationPlan, error) {
	var plan migrationPlan

	s, err := uow.Settings.Get(ctx)
	if err != nil {
		return plan, fmt.Errorf("прочитать настройки базы Music Quiz: %w", err)
	}

	if m.root == "" {
		m.root = s.StorageDir
	}

	if m.dbRoot == "" {
		m.dbRoot = s.StorageDir
	}

	dbRoots := []string{m.dbRoot}
	if absolute, err := filepath.Abs(m.dbRoot); err == nil {
		dbRoots = append(dbRoots, absolute)
	}

	files, total, err := inventory(m.root)
	if err != nil {
		return plan, err
	}

	refs, _, err := uow.Downloads.StorageReferences(ctx)
	if err != nil {
		return plan, err
	}

	mapping, count, err := m.mediaMapping(refs, files, dbRoots)
	if err != nil {
		return plan, err
	}

	_, err = fmt.Fprintf(m.out, "Файлы: %d (%.2f ГиБ). Ссылок к обновлению: %d.\n",
		len(files), float64(total)/(1<<30), count)

	return migrationPlan{files: files, mapping: mapping, count: count}, err
}

func probeBucket(ctx context.Context, bucket *objectstorage.Bucket) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := bucket.Check(ctx); err != nil {
		return err
	}

	file, err := os.CreateTemp("", "quiz-s3-probe-")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.Remove(file.Name())) }()

	content := []byte("Music Quiz S3 probe\n")

	_, err = file.Write(content)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}

	key := ".quiz-probe-" + uuid.NewString()
	digest := md5.Sum(content)

	if err := bucket.PutFile(ctx, key, file.Name(), digest[:]); err != nil {
		return err
	}

	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()

		result = errors.Join(result, bucket.Delete(cleanup, key))
	}()

	if err := bucket.PutFile(ctx, key, file.Name(), digest[:]); !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("бакет не подтвердил защиту от перезаписи: %v", err)
	}

	if err := verifyObject(ctx, bucket, mediaFile{key: key, size: int64(len(content))}, digest); err != nil {
		return err
	}

	obj, err := bucket.Get(ctx, key, http.Header{"Range": {"bytes=0-4"}})
	if err != nil {
		return err
	}

	read, err := io.ReadAll(io.LimitReader(obj.Body, 6))
	if err := errors.Join(err, obj.Body.Close()); err != nil {
		return err
	}

	if !bytes.Equal(read, content[:5]) || aws.ToString(obj.ContentRange) != fmt.Sprintf("bytes 0-4/%d", len(content)) {
		return errors.New("бакет вернул неожиданный результат запроса Range")
	}

	return nil
}

func inventory(root string) (map[string]mediaFile, int64, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, err
	}

	info, err := os.Lstat(root)
	if err != nil {
		return nil, 0, err
	}

	if !info.IsDir() {
		return nil, 0, fmt.Errorf("корень медиатеки %q должен быть каталогом", root)
	}

	files := map[string]mediaFile{}

	var total int64

	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || filename == root {
			return walkErr
		}

		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}

		key := filepath.ToSlash(rel)
		if !objectstorage.ValidKey(key) {
			return fmt.Errorf("файл %q нельзя перенести под исходным именем", rel)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		files[key] = mediaFile{key: key, filename: filename, size: info.Size()}
		total += info.Size()

		return nil
	})

	return files, total, err
}

func (m migration) mediaMapping(
	refs []store.StoredReference, files map[string]mediaFile, roots []string,
) (map[string]string, int, error) {
	mapping := make(map[string]string, len(refs))

	var missing, count int

	for _, ref := range refs {
		if _, err := m.bucket.Key(ref.URI); err == nil {
			continue
		}

		key, ok := oldMediaKey(ref.URI, roots)
		if _, exists := files[key]; !ok || !exists {
			if missing < 30 {
				if _, err := fmt.Fprintf(m.out, "Не найден исходный файл (используется: %t): %q\n", ref.Used, ref.URI); err != nil {
					return nil, 0, err
				}
			}

			missing++

			continue
		}

		mapping[ref.URI] = m.bucket.URI(key)
		count++
	}

	if missing != 0 {
		return nil, 0, fmt.Errorf("перенос остановлен: %d ссылок без исходного файла", missing)
	}

	return mapping, count, nil
}

func oldMediaKey(uri string, roots []string) (string, bool) {
	uri = strings.ReplaceAll(uri, `\`, "/")

	for _, root := range roots {
		root = strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
		if root == "" {
			continue
		}

		if key, ok := strings.CutPrefix(uri, root+"/"); ok && objectstorage.ValidKey(key) {
			return key, true
		}
	}

	return "", false
}

func (m migration) transfer(ctx context.Context, files map[string]mediaFile) error {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(m.jobs)

	var checked atomic.Int64

	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}

		file := files[key]

		g.Go(func() error {
			digest, err := hashFile(file)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			err = verifyObject(ctx, m.bucket, file, digest)
			if m.mode == "copy" && errors.Is(err, fs.ErrNotExist) {
				if err := m.bucket.PutFile(ctx, file.key, file.filename, digest[:]); err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}

				err = verifyObject(ctx, m.bucket, file, digest)
			}

			if err != nil {
				return err
			}

			if n := checked.Add(1); n%250 == 0 {
				_, err := fmt.Fprintf(m.out, "Проверено объектов: %d/%d\n", n, len(files))
				return err
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	if checked.Load() != int64(len(files)) {
		return ctx.Err()
	}

	return nil
}

func hashFile(file mediaFile) ([md5.Size]byte, error) {
	var digest [md5.Size]byte

	in, err := os.Open(file.filename)
	if err != nil {
		return digest, err
	}

	sum := md5.New()

	n, err := io.Copy(sum, in)
	if err := errors.Join(err, in.Close()); err != nil {
		return digest, err
	}

	if n != file.size {
		return digest, fmt.Errorf("исходный файл %q изменился во время переноса", file.key)
	}

	sum.Sum(digest[:0])

	return digest, nil
}

func verifyObject(ctx context.Context, bucket *objectstorage.Bucket, file mediaFile, digest [md5.Size]byte) error {
	info, err := bucket.Head(ctx, file.key)
	if err != nil {
		return err
	}

	checksum := info.Metadata["quiz-md5"]
	if checksum == "" {
		checksum = strings.Trim(aws.ToString(info.ETag), `"`)
	}

	if info.ContentLength == nil || *info.ContentLength != file.size || checksum != fmt.Sprintf("%x", digest) {
		return fmt.Errorf("объект %q отличается от локального файла; перезапись запрещена", file.key)
	}

	return nil
}
