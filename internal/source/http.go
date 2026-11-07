// Package source изолирует API источников музыкальных метаданных.
package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"devkeeper.com/quiz/internal/domain"
)

type jsonClient struct {
	client        *http.Client
	base          string
	token         string
	userAgent     string
	interval      time.Duration
	next          time.Time
	retries       int
	retryDelay    time.Duration
	maxRetryDelay time.Duration
	onQuota       func(context.Context, domain.GeniusQuota) error
}

// HTTPError сообщает вызывающему сервису код ответа внешнего API.
type HTTPError struct {
	Base       string
	Status     int
	RetryAfter time.Duration
}

// Error возвращает безопасное описание ошибки без токена и параметров запроса.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("API %s: HTTP %d", e.Base, e.Status)
}

func (c *jsonClient) get(ctx context.Context, path string, target any) error {
	for attempt := 0; ; attempt++ {
		err := c.getOnce(ctx, path, target)

		var apiErr *HTTPError

		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
			return err
		}

		delay := c.retryWait(apiErr.RetryAfter, attempt)

		c.next = time.Now().Add(delay)
		if attempt >= c.retries {
			return err
		}

		slog.WarnContext(ctx, "источник ограничил запросы; ожидаем повтор",
			"source", c.base, "wait", delay, "retry", attempt+1)

		if waitErr := pause(ctx, delay); waitErr != nil {
			// Сохраняем код 429, чтобы работник вернул исполнителя в очередь при исчерпании времени.
			return fmt.Errorf("%w: ожидание прервано: %v", err, waitErr)
		}
	}
}

func (c *jsonClient) retryWait(header time.Duration, attempt int) time.Duration {
	delay := c.retryDelay
	for range attempt {
		delay = min(delay*2, c.maxRetryDelay)
	}

	return max(header, delay, c.interval)
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
			return time.Duration(seconds) * time.Second
		}
	}

	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now))
	}

	return 0
}

func (c *jsonClient) getOnce(ctx context.Context, path string, target any) (result error) {
	if err := pause(ctx, time.Until(c.next)); err != nil {
		return err
	}

	c.next = time.Now().Add(c.interval)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.base, "/")+path, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, resp.Body.Close()) }()

	if c.onQuota != nil {
		if quota, ok := quotaFromHeaders(resp.Header, time.Now()); ok {
			if err := c.onQuota(ctx, quota); err != nil {
				return fmt.Errorf("сохранение лимита Genius: %w", err)
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return &HTTPError{
			Base: c.base, Status: resp.StatusCode,
			RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil {
		return err
	}

	if len(data) > 10<<20 {
		return fmt.Errorf("ответ API превышает 10 МБ")
	}

	return json.Unmarshal(data, target)
}

func quotaFromHeaders(h http.Header, now time.Time) (domain.GeniusQuota, bool) {
	limit, e1 := strconv.ParseInt(h.Get("X-Ratelimit-Limit"), 10, 64)
	remaining, e2 := strconv.ParseInt(h.Get("X-Ratelimit-Remaining"), 10, 64)
	reset, e3 := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64)

	if e1 != nil || e2 != nil || e3 != nil || limit <= 0 || remaining < 0 || remaining > limit ||
		reset < 0 || reset > int64((1<<63-1)/time.Second) {
		return domain.GeniusQuota{}, false
	}

	return domain.GeniusQuota{
		Limit: limit, Remaining: remaining,
		ResetAt: now.Add(time.Duration(reset) * time.Second), ObservedAt: now,
	}, true
}

func pause(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}

	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
