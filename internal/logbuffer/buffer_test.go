package logbuffer

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRecentBoundedAndOrdered(t *testing.T) {
	b := New(2)
	for i := 1; i <= 3; i++ {
		if _, err := b.Write([]byte(fmt.Sprintf(`{"msg":"%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	rows := b.Recent(10)
	if len(rows) != 2 || string(rows[0]) != `{"msg":"3"}` || string(rows[1]) != `{"msg":"2"}` {
		t.Fatalf("порядок: %s", rows)
	}
	rows[0][0] = 'x'
	if !json.Valid(b.Recent(1)[0]) {
		t.Fatal("буфер изменён извне")
	}
	if len(b.Recent(0)) != 0 {
		t.Fatal("ожидался пустой результат")
	}
}

func TestConcurrentAndLargeRecords(t *testing.T) {
	b := New(20)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if _, err := b.Write([]byte(`{"msg":"test"}`)); err != nil {
					t.Error(err)
				}
				_ = b.Recent(5)
			}
		}()
	}
	wg.Wait()
	if _, err := b.Write([]byte(strings.Repeat("x", maxEntryBytes+1))); err != nil {
		t.Fatal(err)
	}
	rows := b.Recent(20)
	if len(rows) != 20 || !json.Valid(rows[0]) || !strings.Contains(string(rows[0]), "превышает") {
		t.Fatalf("неверный результат: %s", rows[0])
	}
}
