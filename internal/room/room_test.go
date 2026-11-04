package room

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	anet "arena/internal/net"
)

const waitTimeout = 3 * time.Second

// fakeClient — Client с очередью фиксированной ёмкости. Ёмкость 1 моделирует
// клиента, который не вычитывает сообщения: Welcome занимает очередь,
// и первый же снапшот не помещается.
type fakeClient struct {
	msgs chan []byte

	once   sync.Once
	closed chan struct{}
	code   websocket.StatusCode // пишется до close(closed)
}

func newFakeClient(capacity int) *fakeClient {
	return &fakeClient{msgs: make(chan []byte, capacity), closed: make(chan struct{})}
}

func (f *fakeClient) TrySend(msg []byte) bool {
	select {
	case <-f.closed:
		return false
	default:
	}
	select {
	case f.msgs <- msg:
		return true
	default:
		return false
	}
}

func (f *fakeClient) Close(code websocket.StatusCode, _ string) {
	f.once.Do(func() {
		f.code = code
		close(f.closed)
	})
}

func (f *fakeClient) waitClosed(t *testing.T) websocket.StatusCode {
	t.Helper()
	select {
	case <-f.closed:
		return f.code
	case <-time.After(waitTimeout):
		t.Fatal("client was not closed")
		return 0
	}
}

func (f *fakeClient) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

// welcome проверяет, что первое сообщение — Welcome.
func (f *fakeClient) welcome(t *testing.T) anet.Welcome {
	t.Helper()
	select {
	case b := <-f.msgs:
		var w anet.Welcome
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("bad message %q: %v", b, err)
		}
		if w.Type != anet.TypeWelcome {
			t.Fatalf("first message type = %q, want %q", w.Type, anet.TypeWelcome)
		}
		return w
	case <-time.After(waitTimeout):
		t.Fatal("no welcome received")
		return anet.Welcome{}
	}
}

// snapshotUntil читает снапшоты, пока pred не вернёт true.
func (f *fakeClient) snapshotUntil(t *testing.T, pred func(anet.Snapshot) bool) anet.Snapshot {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case b := <-f.msgs:
			var s anet.Snapshot
			if err := json.Unmarshal(b, &s); err != nil {
				t.Fatalf("bad message %q: %v", b, err)
			}
			if s.Type == anet.TypeSnapshot && pred(s) {
				return s
			}
		case <-deadline:
			t.Fatal("expected snapshot not received")
			return anet.Snapshot{}
		}
	}
}

func hasPlayer(s anet.Snapshot, id uint32) bool {
	_, ok := findPlayer(s, id)
	return ok
}

func findPlayer(s anet.Snapshot, id uint32) (anet.PlayerState, bool) {
	for _, p := range s.Players {
		if p.ID == id {
			return p, true
		}
	}
	return anet.PlayerState{}, false
}

func startRoom(t *testing.T) (*Room, context.CancelFunc) {
	t.Helper()
	rm := New("test", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	go rm.Run(ctx)
	t.Cleanup(func() {
		cancel()
		<-rm.Done()
	})
	return rm, cancel
}

func join(t *testing.T, rm *Room, c Client) uint32 {
	t.Helper()
	id, err := rm.Join(context.Background(), c)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	return id
}

func TestJoinSendsWelcomeAndAddsPlayerOnTick(t *testing.T) {
	rm, _ := startRoom(t)
	c := newFakeClient(64)
	id := join(t, rm, c)

	w := c.welcome(t)
	if w.PlayerID != id || w.Room != "test" || w.TickRate != TickRate {
		t.Fatalf("welcome = %+v, want id=%d room=test tickRate=%d", w, id, TickRate)
	}
	c.snapshotUntil(t, func(s anet.Snapshot) bool { return hasPlayer(s, id) })
}

func TestJoinRejectsWhenFull(t *testing.T) {
	rm, _ := startRoom(t)
	ids := map[uint32]bool{}
	for range MaxPlayers {
		id := join(t, rm, newFakeClient(1024))
		if ids[id] {
			t.Fatalf("duplicate player id %d", id)
		}
		ids[id] = true
	}
	if _, err := rm.Join(context.Background(), newFakeClient(1024)); !errors.Is(err, ErrFull) {
		t.Fatalf("join #%d: err = %v, want ErrFull", MaxPlayers+1, err)
	}
}

func TestLeaveFreesSlotAndRemovesPlayer(t *testing.T) {
	rm, _ := startRoom(t)
	observer := newFakeClient(1024)
	obsID := join(t, rm, observer)

	var leaving uint32
	for range MaxPlayers - 1 {
		leaving = join(t, rm, newFakeClient(1024))
	}
	observer.snapshotUntil(t, func(s anet.Snapshot) bool { return hasPlayer(s, leaving) })

	rm.Leave(leaving)
	rm.Leave(leaving) // повторный leave безвреден
	observer.snapshotUntil(t, func(s anet.Snapshot) bool {
		return !hasPlayer(s, leaving) && hasPlayer(s, obsID)
	})

	if _, err := rm.Join(context.Background(), newFakeClient(1024)); err != nil {
		t.Fatalf("slot was not freed after leave: %v", err)
	}
}

func TestInputsAppliedInTick(t *testing.T) {
	rm, _ := startRoom(t)
	c := newFakeClient(1024)
	id := join(t, rm, c)
	first := c.snapshotUntil(t, func(s anet.Snapshot) bool { return hasPlayer(s, id) })
	start, _ := findPlayer(first, id)

	rm.PushInput(anet.PlayerInput{PlayerID: id, Input: anet.Input{Seq: 7, MoveX: 1}})
	got := c.snapshotUntil(t, func(s anet.Snapshot) bool {
		p, ok := findPlayer(s, id)
		return ok && p.LastSeq == 7
	})
	p, _ := findPlayer(got, id)
	if p.X <= start.X {
		t.Fatalf("player did not move right: x %v -> %v", start.X, p.X)
	}

	// Ввод от несуществующего игрока игнорируется.
	rm.PushInput(anet.PlayerInput{PlayerID: 999, Input: anet.Input{Seq: 1, MoveX: 1}})
	s := c.snapshotUntil(t, func(s anet.Snapshot) bool { return s.Tick > got.Tick })
	if hasPlayer(s, 999) || len(s.Players) != 1 {
		t.Fatalf("unexpected players: %+v", s.Players)
	}
}

func TestSnapshotTicksIncrease(t *testing.T) {
	rm, _ := startRoom(t)
	c := newFakeClient(1024)
	join(t, rm, c)
	c.welcome(t)

	var prev uint64
	for range 5 {
		s := c.snapshotUntil(t, func(anet.Snapshot) bool { return true })
		if s.Tick <= prev {
			t.Fatalf("tick did not increase: %d -> %d", prev, s.Tick)
		}
		prev = s.Tick
	}
}

func TestSlowClientIsDropped(t *testing.T) {
	rm, _ := startRoom(t)
	fast := newFakeClient(1024)
	fastID := join(t, rm, fast)

	slow := newFakeClient(1) // Welcome займёт единственный слот
	slowID := join(t, rm, slow)

	if code := slow.waitClosed(t); code != websocket.StatusPolicyViolation {
		t.Fatalf("slow client close code = %v, want %v", code, websocket.StatusPolicyViolation)
	}

	// Медленный игрок исчез из мира, быстрый продолжает получать снапшоты.
	fast.snapshotUntil(t, func(s anet.Snapshot) bool {
		return hasPlayer(s, fastID) && !hasPlayer(s, slowID)
	})
	if fast.isClosed() {
		t.Fatal("fast client must not be closed")
	}

	// Поздний Leave от обработчика уже удалённого клиента безвреден,
	// а освободившийся слот снова доступен.
	rm.Leave(slowID)
	for range MaxPlayers - 1 {
		join(t, rm, newFakeClient(1024))
	}
}

func TestRunStopClosesClientsAndRejectsJoins(t *testing.T) {
	rm, cancel := startRoom(t)
	c := newFakeClient(1024)
	id := join(t, rm, c)

	cancel()
	<-rm.Done()

	if code := c.waitClosed(t); code != websocket.StatusGoingAway {
		t.Fatalf("close code = %v, want %v", code, websocket.StatusGoingAway)
	}
	if _, err := rm.Join(context.Background(), newFakeClient(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("join after stop: err = %v, want ErrClosed", err)
	}

	done := make(chan struct{})
	go func() {
		rm.Leave(id) // не должен блокироваться после остановки
		rm.PushInput(anet.PlayerInput{PlayerID: id})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("Leave/PushInput blocked after room stop")
	}
}

func TestJoinRespectsContext(t *testing.T) {
	// Комната не запущена: Join должен вернуться по отмене контекста.
	rm := New("idle", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := rm.Join(ctx, newFakeClient(1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
