// Package logbuffer хранит ограниченное число последних структурированных записей журнала.
package logbuffer

import (
	"encoding/json"
	"sync"
)

const maxEntryBytes = 16384

// Buffer хранит последние записи до перезапуска процесса.
type Buffer struct {
	mu       sync.RWMutex
	entries  []json.RawMessage
	capacity int
	next     int
	full     bool
}

// New создаёт буфер фиксированной ёмкости.
func New(capacity int) *Buffer {
	if capacity < 1 {
		panic("logbuffer: capacity must be positive")
	}
	return &Buffer{entries: make([]json.RawMessage, capacity), capacity: capacity}
}

// Write добавляет одну JSON-запись slog; слишком длинные записи заменяются уведомлением.
func (b *Buffer) Write(p []byte) (int, error) {
	raw := json.RawMessage(p)
	if len(p) > maxEntryBytes {
		raw = json.RawMessage(`{"level":"WARN","msg":"Запись журнала превышает допустимый размер"}`)
	}
	if !json.Valid(raw) {
		raw = json.RawMessage(`{"level":"WARN","msg":"Не удалось прочитать запись журнала"}`)
	}
	copied := append(json.RawMessage(nil), raw...)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[b.next] = copied
	b.next = (b.next + 1) % b.capacity
	if b.next == 0 {
		b.full = true
	}
	return len(p), nil
}

// Recent возвращает последние записи от новых к старым.
func (b *Buffer) Recent(limit int) []json.RawMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := b.next
	if b.full {
		n = b.capacity
	}
	n = min(n, max(0, limit))
	result := make([]json.RawMessage, 0, n)
	for i := 0; i < n; i++ {
		index := (b.next - 1 - i + b.capacity) % b.capacity
		result = append(result, append(json.RawMessage(nil), b.entries[index]...))
	}
	return result
}
