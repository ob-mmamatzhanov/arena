package leaderboard

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// stores — обе реализации Store; контрактные тесты гоняются на каждой.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return map[string]Store{"memory": NewMemory(), "redis": NewRedis(rdb)}
}

func TestStoreAddScoreAndTop(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			top, err := st.Top(ctx, 10)
			if err != nil || top == nil || len(top) != 0 {
				t.Fatalf("пустая таблица: %v, %v; want [] без ошибки", top, err)
			}

			must := func(key, name string, score int) {
				t.Helper()
				if err := st.AddScore(ctx, key, name, score); err != nil {
					t.Fatal(err)
				}
			}
			must("k1", "Alice", 1)
			must("k2", "Bob", 1)
			must("k1", "Alice", 1)
			must("k3", "Carol", 5)
			must("k2", "Bobby", 1) // имя перезаписывается последним

			top, err = st.Top(ctx, 10)
			if err != nil {
				t.Fatal(err)
			}
			want := []Entry{{"Carol", 5}, {"Bobby", 2}, {"Alice", 2}}
			if !reflect.DeepEqual(top, want) {
				t.Fatalf("Top(10) = %v, want %v", top, want)
			}

			top, err = st.Top(ctx, 1)
			if err != nil || !reflect.DeepEqual(top, want[:1]) {
				t.Fatalf("Top(1) = %v, %v; want %v", top, err, want[:1])
			}
			if top, err := st.Top(ctx, 0); err != nil || len(top) != 0 {
				t.Fatalf("Top(0) = %v, %v; want []", top, err)
			}
		})
	}
}

// Записи в Redis лежат в ожидаемых ключах — на них может опираться внешний код.
func TestRedisKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	if err := NewRedis(rdb).AddScore(context.Background(), "k1", "Alice", 3); err != nil {
		t.Fatal(err)
	}
	if s, err := mr.ZScore("lb:scores", "k1"); err != nil || s != 3 {
		t.Fatalf("ZSCORE lb:scores k1 = %v, %v; want 3", s, err)
	}
	if n := mr.HGet("lb:names", "k1"); n != "Alice" {
		t.Fatalf("HGET lb:names k1 = %q, want Alice", n)
	}
}

// logBuf — логгер в буфер, чтобы проверить содержимое лога.
func logBuf() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestOpenSelectsStore(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	mr.RequireUserAuth("arena", "s3cret-pass")

	t.Run("empty URL → memory", func(t *testing.T) {
		log, buf := logBuf()
		if _, ok := Open(ctx, "", log).(*Memory); !ok {
			t.Fatal("want *Memory")
		}
		if !strings.Contains(buf.String(), "store=memory") {
			t.Fatalf("лог без выбранного store: %s", buf)
		}
	})

	t.Run("reachable redis → redis, host без пароля", func(t *testing.T) {
		log, buf := logBuf()
		st := Open(ctx, "redis://arena:s3cret-pass@"+mr.Addr()+"/0", log)
		r, ok := st.(*Redis)
		if !ok {
			t.Fatalf("want *Redis, got %T; log: %s", st, buf)
		}
		defer r.Close()
		out := buf.String()
		if !strings.Contains(out, "store=redis") || !strings.Contains(out, "host="+mr.Addr()) {
			t.Fatalf("лог без store/host: %s", out)
		}
		if strings.Contains(out, "s3cret-pass") || strings.Contains(out, "arena:") {
			t.Fatalf("в лог попали учётные данные: %s", out)
		}
	})

	for name, url := range map[string]string{
		"wrong password → memory": "redis://arena:wrong-pass@" + mr.Addr(),
		"unreachable → memory":    "redis://arena:s3cret-pass@127.0.0.1:1",
		"invalid URL → memory":    "http://arena:s3cret-pass@host:6379",
		"broken URL → memory":     "redis://arena:s3cret-pass@[::1",
	} {
		t.Run(name, func(t *testing.T) {
			log, buf := logBuf()
			if st := Open(ctx, url, log); reflect.TypeOf(st) != reflect.TypeOf(&Memory{}) {
				t.Fatalf("want *Memory, got %T", st)
			}
			out := buf.String()
			if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "store=memory") {
				t.Fatalf("ошибка и откат на память не залогированы: %s", out)
			}
			if strings.Contains(out, "s3cret-pass") || strings.Contains(out, "wrong-pass") {
				t.Fatalf("в лог попал пароль: %s", out)
			}
		})
	}
}
