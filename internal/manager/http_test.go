package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	anet "arena/internal/net"
)

type testServer struct {
	m   *Manager
	clk *fakeClock
	url string
}

func startHTTP(t *testing.T) *testServer {
	t.Helper()
	m, clk := testManager(t)
	mux := http.NewServeMux()
	m.Register(mux, HTTPConfig{OriginPatterns: []string{"game.example"}, ClientIPHeader: "X-Test-IP"})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close) // после Shutdown менеджера (Cleanup — в обратном порядке)
	return &testServer{m: m, clk: clk, url: ts.URL}
}

func (s *testServer) do(t *testing.T, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (s *testServer) create(t *testing.T, body string, ip string) (int, createResp) {
	t.Helper()
	resp, b := s.do(t, http.MethodPost, "/rooms", body, map[string]string{"Content-Type": "application/json", "X-Test-IP": ip})
	var cr createResp
	if resp.StatusCode == http.StatusCreated {
		if err := json.Unmarshal([]byte(b), &cr); err != nil {
			t.Fatalf("ответ %q: %v", b, err)
		}
	}
	return resp.StatusCode, cr
}

func TestHTTPCreateAndList(t *testing.T) {
	s := startHTTP(t)
	status, pub := s.create(t, `{"mode":"zombie","maxPlayers":4}`, "1.1.1.1")
	if status != http.StatusCreated || pub.ID == "" || pub.Code != "" {
		t.Fatalf("публичная: %d %+v", status, pub)
	}
	status, priv := s.create(t, `{"mode":"arena","maxPlayers":2,"private":true}`, "1.1.1.1")
	if status != http.StatusCreated || priv.Code == "" {
		t.Fatalf("приватная: %d %+v", status, priv)
	}

	resp, body := s.do(t, http.MethodGet, "/rooms", "", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("GET /rooms: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("GET /rooms = %s, want одну публичную", body)
	}
	want := map[string]any{"id": pub.ID, "mode": "zombie", "phase": "play", "players": 0.0, "maxPlayers": 4.0}
	for k, v := range want {
		if list[0][k] != v {
			t.Fatalf("GET /rooms[0].%s = %v, want %v (%s)", k, list[0][k], v, body)
		}
	}
	if len(list[0]) != len(want) {
		t.Fatalf("лишние поля: %s", body)
	}

	resp, body = s.do(t, http.MethodGet, "/rooms/by-code/"+strings.ToLower(priv.Code), "", nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"id":"`+priv.ID+`"}` {
		t.Fatalf("by-code: %d %s", resp.StatusCode, body)
	}
	if resp, _ = s.do(t, http.MethodGet, "/rooms/by-code/ZZZZ", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("by-code неизвестного: %d, want 404", resp.StatusCode)
	}

	// Пустой список — массив, не null.
	s2 := startHTTP(t)
	if _, body := s2.do(t, http.MethodGet, "/rooms", "", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("пустой GET /rooms = %q", body)
	}
}

func TestHTTPCreateValidation(t *testing.T) {
	s := startHTTP(t)
	for i, body := range []string{
		`{"mode":"ctf","maxPlayers":4}`,
		`{"maxPlayers":4}`,
		`{"mode":"arena","maxPlayers":1}`,
		`{"mode":"arena","maxPlayers":9}`,
		`{"mode":"arena"}`,
		`not json`,
	} {
		// разные IP, чтобы не упереться в лимит
		if status, _ := s.create(t, body, "10.0.0."+string(rune('1'+i))); status != http.StatusBadRequest {
			t.Errorf("POST %s: %d, want 400", body, status)
		}
	}
}

func TestHTTPCreateRateLimit(t *testing.T) {
	s := startHTTP(t)
	body := `{"mode":"arena","maxPlayers":2}`
	for i := range CreateLimit {
		if status, _ := s.create(t, body, "1.2.3.4"); status != http.StatusCreated {
			t.Fatalf("запрос #%d: %d", i+1, status)
		}
	}
	resp, _ := s.do(t, http.MethodPost, "/rooms", body, map[string]string{"X-Test-IP": "1.2.3.4"})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("запрос #%d: %d Retry-After=%q, want 429", CreateLimit+1, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if status, _ := s.create(t, body, "5.6.7.8"); status != http.StatusCreated {
		t.Fatalf("другой IP: %d, want 201", status)
	}
	s.clk.Advance(CreateWindow)
	if status, _ := s.create(t, body, "1.2.3.4"); status != http.StatusCreated {
		t.Fatalf("после окна: %d, want 201", status)
	}
}

func TestHTTPCORS(t *testing.T) {
	s := startHTTP(t)
	resp, _ := s.do(t, http.MethodGet, "/rooms", "", map[string]string{"Origin": "https://game.example"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://game.example" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	resp, _ = s.do(t, http.MethodOptions, "/rooms", "", map[string]string{
		"Origin": "https://game.example", "Access-Control-Request-Method": "POST",
	})
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") ||
		resp.Header.Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = s.do(t, http.MethodPost, "/rooms", `{"mode":"arena","maxPlayers":2}`, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden || len(s.m.List()) != 0 {
		t.Fatalf("POST с чужого Origin: %d, комнат %d", resp.StatusCode, len(s.m.List()))
	}
}

func (s *testServer) dial(t *testing.T, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.url, "http")+"/ws"+query, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", query, err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// joinWS шлёт join и возвращает Welcome.
func joinWS(t *testing.T, ws *websocket.Conn) anet.Welcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := wsjson.Write(ctx, ws, anet.Join{Type: anet.TypeJoin}); err != nil {
		t.Fatal(err)
	}
	var w anet.Welcome
	if err := wsjson.Read(ctx, ws, &w); err != nil {
		t.Fatal(err)
	}
	if w.Type != anet.TypeWelcome {
		t.Fatalf("первое сообщение %+v, want welcome", w)
	}
	return w
}

func closeCode(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("сервер не закрыл соединение")
			}
			return websocket.CloseStatus(err)
		}
	}
}

func TestWSRouting(t *testing.T) {
	s := startHTTP(t)

	if code := closeCode(t, s.dial(t, "?room=nope1234")); code != CloseRoomNotFound {
		t.Fatalf("?room=неизвестная: close %d, want %d", code, CloseRoomNotFound)
	}
	if code := closeCode(t, s.dial(t, "?mode=ctf")); code != CloseBadRequest {
		t.Fatalf("?mode=ctf: close %d, want %d", code, CloseBadRequest)
	}

	def := joinWS(t, s.dial(t, ""))
	if def.Mode != ModeArena || s.m.Get(def.Room) == nil {
		t.Fatalf("без параметров: %+v", def)
	}
	z := joinWS(t, s.dial(t, "?mode=zombie"))
	if z.Mode != ModeZombie || z.Room == def.Room {
		t.Fatalf("?mode=zombie: %+v", z)
	}
	again := joinWS(t, s.dial(t, "?mode=arena"))
	if again.Room != def.Room {
		t.Fatalf("автозаполнение создало вторую arena: %s и %s", def.Room, again.Room)
	}

	priv, err := s.m.Create(Opts{Mode: ModeArena, MaxPlayers: 2, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if w := joinWS(t, s.dial(t, "?room="+priv.Name())); w.Room != priv.Name() {
		t.Fatalf("?room=%s: попал в %s", priv.Name(), w.Room)
	}
	joinWS(t, s.dial(t, "?room="+priv.Name()))
	full := s.dial(t, "?room="+priv.Name())
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := wsjson.Write(ctx, full, anet.Join{Type: anet.TypeJoin}); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, full); code != websocket.StatusTryAgainLater {
		t.Fatalf("полная комната: close %d, want TryAgainLater", code)
	}
	waitFor(t, "игроки в метрике", func() bool { return priv.Players() == 2 })
}
