package leaderboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// failStore — Store, у которого Top всегда падает.
type failStore struct{}

func (failStore) AddScore(context.Context, string, string, int) error { return nil }

func (failStore) Top(context.Context, int) ([]Entry, error) { return nil, errors.New("down") }

func TestHandler(t *testing.T) {
	st := NewMemory()
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		_ = st.AddScore(ctx, name, strings.ToUpper(name), i+1)
	}
	h := Handler(st, []string{"localhost:*", "game.example.com"}, slog.New(slog.DiscardHandler))

	get := func(h http.Handler, target, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get(h, "/leaderboard", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var top []Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatal(err)
	}
	if want := []Entry{{"C", 3}, {"B", 2}, {"A", 1}}; !reflect.DeepEqual(top, want) {
		t.Fatalf("top = %v, want %v", top, want)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `[{"name":"C","score":3},{"name":"B","score":2},{"name":"A","score":1}]` {
		t.Fatalf("JSON = %s", got)
	}

	rec = get(h, "/leaderboard?n=2", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil || len(top) != 2 {
		t.Fatalf("n=2: %s", rec.Body)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if rec := get(h, "/leaderboard?n="+bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("n=%s: status %d, want 400", bad, rec.Code)
		}
	}

	empty := Handler(NewMemory(), nil, slog.New(slog.DiscardHandler))
	if rec := get(empty, "/leaderboard", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("пустая таблица = %s, want []", rec.Body)
	}

	down := Handler(failStore{}, nil, slog.New(slog.DiscardHandler))
	if rec := get(down, "/leaderboard", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("хранилище недоступно: status %d, want 503", rec.Code)
	}
}

// CORS — те же правила, что у WebSocket: шаблоны хоста и собственный хост.
func TestHandlerCORS(t *testing.T) {
	h := Handler(NewMemory(), []string{"localhost:*", "game.example.com"}, slog.New(slog.DiscardHandler))
	tests := []struct {
		origin  string
		allowed bool
	}{
		{"http://localhost:5173", true},
		{"https://GAME.example.com", true},
		{"http://example.com", true}, // собственный хост запроса (httptest — example.com)
		{"https://evil.com", false},
		{"https://game.example.com.evil.com", false},
		{"not a url", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/leaderboard", nil)
		req.Header.Set("Origin", tt.origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("Access-Control-Allow-Origin")
		if tt.allowed && got != tt.origin || !tt.allowed && got != "" {
			t.Errorf("Origin %q: Allow-Origin = %q, allowed=%v", tt.origin, got, tt.allowed)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Errorf("Origin %q: нет Vary: Origin", tt.origin)
		}
	}
}
