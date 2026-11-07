package room_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	anet "arena/internal/net"
	"arena/internal/room"
)

const timeout = 3 * time.Second

type server struct {
	url  string
	room *room.Room
	stop context.CancelFunc
}

// startServer поднимает httptest-сервер с одной комнатой и реальным WS-обработчиком.
func startServer(t *testing.T) *server {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	rm := room.New("lobby", log)
	ctx, cancel := context.WithCancel(context.Background())
	go rm.Run(ctx)

	ts := httptest.NewServer(room.Handler(rm, anet.AcceptOptions{}, log))
	t.Cleanup(func() {
		// Тот же порядок, что в main: сначала комната закрывает соединения.
		cancel()
		<-rm.Done()
		ts.Close()
	})
	return &server{url: "ws" + strings.TrimPrefix(ts.URL, "http"), room: rm, stop: cancel}
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
	id uint32
}

func dial(t *testing.T, s *server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, s.url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// connect подключается, шлёт join без имени и ключа и дожидается Welcome.
func connect(t *testing.T, s *server) *client {
	t.Helper()
	c, _ := connectAs(t, s, anet.Join{Type: anet.TypeJoin})
	return c
}

// connectAs подключается с сообщением join j и возвращает клиента и Welcome.
func connectAs(t *testing.T, s *server, j anet.Join) (*client, anet.Welcome) {
	t.Helper()
	c := &client{t: t, ws: dial(t, s)}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Write(ctx, c.ws, j); err != nil {
		t.Fatalf("write join: %v", err)
	}
	var w anet.Welcome
	c.readJSON(&w)
	if w.Type != anet.TypeWelcome || w.Room != "lobby" || w.TickRate != room.TickRate || w.PlayerID == 0 {
		t.Fatalf("bad welcome: %+v", w)
	}
	c.id = w.PlayerID
	return c, w
}

func (c *client) readJSON(v any) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Read(ctx, c.ws, v); err != nil {
		c.t.Fatalf("player %d read: %v", c.id, err)
	}
}

func (c *client) send(in anet.Input) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Write(ctx, c.ws, in); err != nil {
		c.t.Fatalf("player %d write: %v", c.id, err)
	}
}

// snapshotUntil читает снапшоты, пока pred не вернёт true.
func (c *client) snapshotUntil(pred func(anet.Snapshot) bool) anet.Snapshot {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var raw json.RawMessage
		c.readJSON(&raw)
		var s anet.Snapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			c.t.Fatalf("bad message %s: %v", raw, err)
		}
		if s.Type == anet.TypeSnapshot && pred(s) {
			return s
		}
	}
	c.t.Fatalf("player %d: expected snapshot not received", c.id)
	return anet.Snapshot{}
}

// waitClose читает до закрытия соединения и возвращает код закрытия.
func waitClose(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("connection was not closed by server")
			}
			return websocket.CloseStatus(err)
		}
	}
}

func player(s anet.Snapshot, id uint32) (anet.PlayerState, bool) {
	for _, p := range s.Players {
		if p.ID == id {
			return p, true
		}
	}
	return anet.PlayerState{}, false
}

func hasAll(ids ...uint32) func(anet.Snapshot) bool {
	return func(s anet.Snapshot) bool {
		for _, id := range ids {
			if _, ok := player(s, id); !ok {
				return false
			}
		}
		return true
	}
}

func TestPlayersSeeEachOtherAndMove(t *testing.T) {
	s := startServer(t)
	a, b := connect(t, s), connect(t, s)
	if a.id == b.id {
		t.Fatalf("players got the same id %d", a.id)
	}

	first := b.snapshotUntil(hasAll(a.id, b.id))
	a.snapshotUntil(hasAll(a.id, b.id))
	startA, _ := player(first, a.id)
	startB, _ := player(first, b.id)

	// A идёт вправо; B видит это в своих снапшотах вместе с подтверждением seq.
	for seq := uint32(1); seq <= 3; seq++ {
		a.send(anet.Input{Seq: seq, MoveX: 1})
	}
	got := b.snapshotUntil(func(s anet.Snapshot) bool {
		p, ok := player(s, a.id)
		return ok && p.LastSeq == 3
	})
	pa, _ := player(got, a.id)
	pb, _ := player(got, b.id)
	if pa.X <= startA.X || pa.Y != startA.Y {
		t.Fatalf("A moved wrong: %+v -> %+v", startA, pa)
	}
	if pb.X != startB.X || pb.Y != startB.Y || pb.LastSeq != 0 {
		t.Fatalf("B must not move: %+v -> %+v", startB, pb)
	}
}

func TestNinthPlayerRejected(t *testing.T) {
	s := startServer(t)
	for range room.MaxPlayers {
		connect(t, s)
	}
	ws := dialJoin(t, s)
	if code := waitClose(t, ws); code != websocket.StatusTryAgainLater {
		t.Fatalf("close code = %v, want %v", code, websocket.StatusTryAgainLater)
	}
}

func TestDisconnectRemovesPlayer(t *testing.T) {
	s := startServer(t)
	a, b := connect(t, s), connect(t, s)
	a.snapshotUntil(hasAll(a.id, b.id))

	if err := b.ws.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("close: %v", err)
	}
	a.snapshotUntil(func(s anet.Snapshot) bool {
		_, hasB := player(s, b.id)
		return !hasB && hasAll(a.id)(s)
	})

	// Слот освободился: можно снова набрать полную комнату.
	for range room.MaxPlayers - 1 {
		connect(t, s)
	}
}

func TestMalformedInputClosesConnection(t *testing.T) {
	s := startServer(t)
	a, b := connect(t, s), connect(t, s)
	a.snapshotUntil(hasAll(a.id, b.id))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := b.ws.Write(ctx, websocket.MessageText, []byte("{not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code := waitClose(t, b.ws); code != websocket.StatusUnsupportedData {
		t.Fatalf("close code = %v, want %v", code, websocket.StatusUnsupportedData)
	}
	a.snapshotUntil(func(s anet.Snapshot) bool {
		_, hasB := player(s, b.id)
		return !hasB
	})
}

func TestShutdownClosesClientsWithGoingAway(t *testing.T) {
	s := startServer(t)
	a, b := connect(t, s), connect(t, s)
	a.snapshotUntil(hasAll(a.id, b.id))

	s.stop()
	<-s.room.Done()

	for _, c := range []*client{a, b} {
		if code := waitClose(t, c.ws); code != websocket.StatusGoingAway {
			t.Fatalf("player %d close code = %v, want %v", c.id, code, websocket.StatusGoingAway)
		}
	}

	// После остановки новые подключения сразу отклоняются.
	ws := dialJoin(t, s)
	if code := waitClose(t, ws); code != websocket.StatusGoingAway {
		t.Fatalf("late join close code = %v, want %v", code, websocket.StatusGoingAway)
	}
}

// dialJoin подключается и сразу шлёт пустой join, не дожидаясь ответа.
func dialJoin(t *testing.T, s *server) *websocket.Conn {
	t.Helper()
	ws := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Write(ctx, ws, anet.Join{Type: anet.TypeJoin}); err != nil {
		t.Fatalf("write join: %v", err)
	}
	return ws
}

// Имя и ключ из join возвращаются в Welcome, имя и цвет видны в снапшотах
// других игроков; ключ в снапшот не попадает.
func TestJoinProfileInWelcomeAndSnapshot(t *testing.T) {
	s := startServer(t)
	a, wa := connectAs(t, s, anet.Join{Type: anet.TypeJoin, Name: "  Alice  ", PlayerKey: "key-alice"})
	b, wb := connectAs(t, s, anet.Join{Type: anet.TypeJoin, Name: "x"})

	if wa.Name != "Alice" || wa.PlayerKey != "key-alice" {
		t.Fatalf("welcome A: name %q key %q, want Alice / key-alice", wa.Name, wa.PlayerKey)
	}
	if want := fmt.Sprintf("Player_%d", b.id); wb.Name != want {
		t.Fatalf("welcome B name = %q, want %q", wb.Name, want)
	}
	if wb.PlayerKey == "" {
		t.Fatal("пустой playerKey должен генерироваться")
	}
	if wa.Color == wb.Color {
		t.Fatalf("оба игрока получили цвет %d", wa.Color)
	}

	var raw json.RawMessage
	got := b.snapshotUntil(func(sn anet.Snapshot) bool {
		if !hasAll(a.id, b.id)(sn) {
			return false
		}
		raw, _ = json.Marshal(sn)
		return true
	})
	pa, _ := player(got, a.id)
	if pa.Name != "Alice" || pa.Color != wa.Color {
		t.Fatalf("snapshot A = %+v, want name Alice color %d", pa, wa.Color)
	}
	if strings.Contains(string(raw), "key-alice") {
		t.Fatalf("playerKey утёк в снапшот: %s", raw)
	}
}

// Без join соединение закрывается по таймауту, игрок в комнату не попадает.
func TestJoinTimeoutClosesConnection(t *testing.T) {
	defer room.SetJoinTimeout(100 * time.Millisecond)()
	s := startServer(t)
	ws := dial(t, s)
	if code := waitClose(t, ws); code != websocket.StatusPolicyViolation {
		t.Fatalf("close code = %v, want %v", code, websocket.StatusPolicyViolation)
	}
	// Слот не занят: можно набрать полную комнату.
	for range room.MaxPlayers {
		connect(t, s)
	}
}

// Первым пришёл Input вместо join — соединение закрывается.
func TestInputBeforeJoinClosesConnection(t *testing.T) {
	s := startServer(t)
	ws := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Write(ctx, ws, anet.Input{Seq: 1, MoveX: 1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code := waitClose(t, ws); code != websocket.StatusPolicyViolation {
		t.Fatalf("close code = %v, want %v", code, websocket.StatusPolicyViolation)
	}
}
