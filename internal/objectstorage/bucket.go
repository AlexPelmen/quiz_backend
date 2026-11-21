// Package objectstorage подключает приватный S3-совместимый бакет.
package objectstorage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Bucket выполняет операции в одном настроенном бакете.
type Bucket struct {
	name   string
	client *s3.Client
}

// FromEnv настраивает клиент по переменным окружения бэкенда.
func FromEnv() (*Bucket, error) {
	name := os.Getenv("S3_BUCKET")
	id, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")

	if name == "" || !fs.ValidPath(name) || strings.ContainsAny(name, "/\\?#: ") || id == "" || secret == "" {
		return nil, errors.New("для S3 требуются S3_BUCKET, AWS_ACCESS_KEY_ID и AWS_SECRET_ACCESS_KEY")
	}

	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://storage.yandexcloud.net"
	}

	address, err := url.Parse(endpoint)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil ||
		address.RawQuery != "" || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
		return nil, errors.New("S3_ENDPOINT должен быть HTTPS-адресом сервиса")
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "ru-central1"
	}

	return &Bucket{name: name, client: s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint), Region: region, UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(id, secret, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		Retryer:                    aws.NopRetryer{},
		HTTPClient:                 &http.Client{Timeout: 2 * time.Minute},
	})}, nil
}

// URI возвращает ссылку на объект для хранения в PostgreSQL.
func (b *Bucket) URI(key string) string {
	return "s3://" + b.name + "/" + key
}

// Key проверяет принадлежность ссылки текущему бакету и возвращает ключ.
func (b *Bucket) Key(uri string) (string, error) {
	key, ok := strings.CutPrefix(uri, b.URI(""))
	if !ok || !ValidKey(key) {
		return "", fmt.Errorf("недопустимая ссылка на объект %q", uri)
	}

	return key, nil
}

// ValidKey проверяет относительный путь медиа без скрытых и служебных файлов.
func ValidKey(key string) bool {
	if !fs.ValidPath(key) || key == "." || len(key) > 1024 || strings.Contains(key, `\`) {
		return false
	}

	for part := range strings.SplitSeq(key, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}

	return true
}

// Check проверяет доступ к списку объектов.
func (b *Bucket) Check(ctx context.Context) error {
	_, err := b.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.name), MaxKeys: aws.Int32(1),
	})

	return storageError("проверка бакета", b.name, err)
}

// List перечисляет объекты, включая все страницы ответа.
func (b *Bucket) List(ctx context.Context, visit func(key string, size int64)) error {
	pages := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{Bucket: aws.String(b.name)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return storageError("список объектов", b.name, err)
		}

		for _, item := range page.Contents {
			key := aws.ToString(item.Key)
			if ValidKey(key) {
				visit(key, aws.ToInt64(item.Size))
			}
		}
	}

	return nil
}

// Head возвращает метаданные объекта.
func (b *Bucket) Head(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)})
	return out, storageError("метаданные объекта", key, err)
}

// Get возвращает поток объекта и поддерживает условные запросы браузера.
func (b *Bucket) Get(ctx context.Context, key string, headers http.Header) (*s3.GetObjectOutput, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)}
	if value := headers.Get("If-None-Match"); value != "" {
		in.IfNoneMatch = aws.String(value)
	}

	if value := headers.Get("If-Match"); value != "" {
		in.IfMatch = aws.String(value)
	}

	if value, err := http.ParseTime(headers.Get("If-Modified-Since")); err == nil {
		in.IfModifiedSince = &value
	}

	if value, err := http.ParseTime(headers.Get("If-Unmodified-Since")); err == nil {
		in.IfUnmodifiedSince = &value
	}

	if value := headers.Get("Range"); value != "" && !strings.Contains(value, ",") {
		validator := headers.Get("If-Range")
		if validator != "" {
			info, err := b.Head(ctx, key)
			if err != nil {
				return nil, err
			}

			date, dateErr := http.ParseTime(validator)

			dateMatches := dateErr == nil && info.LastModified != nil && date.Equal(*info.LastModified)
			if validator != aws.ToString(info.ETag) && !dateMatches {
				value = ""
			}
		}

		if value != "" {
			in.Range = aws.String(value)
		}
	}

	out, err := b.client.GetObject(ctx, in)

	return out, storageError("чтение объекта", key, err)
}

// PutFile создаёт объект из файла, защищая существующий объект от перезаписи.
func (b *Bucket) PutFile(ctx context.Context, key, filename string, digest []byte) (result error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, file.Close()) }()

	in := &s3.PutObjectInput{
		Bucket: aws.String(b.name), Key: aws.String(key), Body: file, IfNoneMatch: aws.String("*"),
		ContentType: aws.String(ContentType(key)),
	}
	if len(digest) != 0 {
		in.ContentMD5 = aws.String(base64.StdEncoding.EncodeToString(digest))
		in.Metadata = map[string]string{"quiz-md5": fmt.Sprintf("%x", digest)}
	}

	_, err = b.client.PutObject(ctx, in)
	if Status(err) == http.StatusPreconditionFailed {
		return fmt.Errorf("%s: %w", key, fs.ErrExist)
	}

	return storageError("запись объекта", key, err)
}

// Delete удаляет объект по ключу.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)})
	return storageError("удаление объекта", key, err)
}

// SaveLocal загружает объект во временный локальный файл и ограничивает его размер.
func (b *Bucket) SaveLocal(ctx context.Context, key, filename string, limit int64) (result error) {
	out, err := b.Get(ctx, key, nil)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, out.Body.Close()) }()

	file, err := os.Create(filename)
	if err != nil {
		return err
	}

	n, err := io.Copy(file, io.LimitReader(out.Body, limit+1))
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}

	if n > limit || out.ContentLength != nil && n != *out.ContentLength {
		return fmt.Errorf("размер полученного объекта %q не соответствует пределу или метаданным", key)
	}

	return nil
}

// Status возвращает HTTP-статус ответа S3, если он известен.
func Status(err error) int {
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) {
		return response.HTTPStatusCode()
	}

	return 0
}

// ContentType определяет заголовок медиа по расширению ключа.
func ContentType(key string) string {
	extension := strings.ToLower(path.Ext(key))
	if extension == ".mp3" {
		return "audio/mpeg"
	}

	if contentType := mime.TypeByExtension(extension); contentType != "" {
		return contentType
	}

	return "application/octet-stream"
}

func storageError(operation, key string, err error) error {
	if err == nil {
		return nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
		err = errors.Join(fs.ErrNotExist, err)
	}

	return fmt.Errorf("S3 %s %q: %w", operation, key, err)
}
