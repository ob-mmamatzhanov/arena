package room

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"arena/internal/game"
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
	return startRoomWith(t, nil)
}

// startRoomWith — комната с миром на конфиге cfg (nil — DefaultConfig).
func startRoomWith(t *testing.T, cfg *game.Config) (*Room, context.CancelFunc) {
	t.Helper()
	rm := New("test", slog.New(slog.DiscardHandler))
	if cfg != nil {
		rm.world = game.NewWorld(*cfg) // до Run: мир ещё не принадлежит горутине комнаты
	}
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

// В Welcome у каждого препятствия есть поле kind; пустой вид в конфиге
// отдаётся как "brick".
func TestWelcomeObstacleKindJSON(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Obstacles = []game.Obstacle{
		{X: 10, Y: 20, W: 30, H: 40},
		{X: 100, Y: 200, W: 32, H: 32, Kind: game.ObstacleCrate},
	}
	rm, _ := startRoomWith(t, &cfg)
	c := newFakeClient(64)
	join(t, rm, c)

	var raw struct {
		Obstacles []map[string]any `json:"obstacles"`
	}
	select {
	case b := <-c.msgs:
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("bad message %q: %v", b, err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no welcome received")
	}
	want := []map[string]any{
		{"x": 10.0, "y": 20.0, "w": 30.0, "h": 40.0, "kind": "brick"},
		{"x": 100.0, "y": 200.0, "w": 32.0, "h": 32.0, "kind": "crate"},
	}
	if len(raw.Obstacles) != len(want) {
		t.Fatalf("obstacles = %v, want %v", raw.Obstacles, want)
	}
	for i := range want {
		if len(raw.Obstacles[i]) != len(want[i]) {
			t.Errorf("obstacle %d keys = %v, want %v", i, raw.Obstacles[i], want[i])
		}
		for k, v := range want[i] {
			if raw.Obstacles[i][k] != v {
				t.Errorf("obstacle %d: %s = %v, want %v", i, k, raw.Obstacles[i][k], v)
			}
		}
	}
}

func TestJoinSendsWelcomeAndAddsPlayerOnTick(t *testing.T) {
	rm, _ := startRoom(t)
	c := newFakeClient(64)
	id := join(t, rm, c)

	w := c.welcome(t)
	if w.PlayerID != id || w.Room != "test" || w.TickRate != TickRate {
		t.Fatalf("welcome = %+v, want id=%d room=test tickRate=%d", w, id, TickRate)
	}
	if cfg := rm.world.Config(); w.Width != cfg.Width || w.Height != cfg.Height {
		t.Fatalf("welcome size = %vx%v, want world size %vx%v", w.Width, w.Height, cfg.Width, cfg.Height)
	}
	if cfg := rm.world.Config(); w.PlayerSpeed != cfg.PlayerSpeed || w.PlayerRadius != cfg.PlayerRadius {
		t.Fatalf("welcome speed/radius = %v/%v, want %v/%v", w.PlayerSpeed, w.PlayerRadius, cfg.PlayerSpeed, cfg.PlayerRadius)
	}
	obs := rm.world.Config().Obstacles
	if len(obs) == 0 || len(w.Obstacles) != len(obs) {
		t.Fatalf("welcome obstacles = %+v, want %d from world config", w.Obstacles, len(obs))
	}
	for i, o := range obs {
		if got := w.Obstacles[i]; got != (anet.Obstacle{X: o.X, Y: o.Y, W: o.W, H: o.H, Kind: string(o.KindOrDefault())}) {
			t.Fatalf("welcome obstacle %d = %+v, want %+v", i, got, o)
		}
	}
	c.snapshotUntil(t, func(s anet.Snapshot) bool {
		p, ok := findPlayer(s, id)
		return ok && p.HP == game.StartHP
	})
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

func TestFireProducesBulletsAndHits(t *testing.T) {
	// Открытая карта: на карте по умолчанию спавны намеренно за укрытиями,
	// и прямой линии между первыми двумя игроками нет. Тест — про выстрел и попадание.
	cfg := game.DefaultConfig()
	cfg.Map = game.Map{Name: "open", Width: 800, Height: 600}
	rm, _ := startRoomWith(t, &cfg)
	ca, cb := newFakeClient(1024), newFakeClient(1024)
	a, b := join(t, rm, ca), join(t, rm, cb)
	// Ждём конца неуязвимости b после входа: иначе пули гасли бы без урона.
	s := ca.snapshotUntil(t, func(s anet.Snapshot) bool {
		p, ok := findPlayer(s, b)
		return hasPlayer(s, a) && ok && !p.Invuln
	})
	pa, _ := findPlayer(s, a)
	pb, _ := findPlayer(s, b)

	// a целится в стоящего b и зажимает огонь.
	angle := math.Atan2(pb.Y-pa.Y, pb.X-pa.X)
	rm.PushInput(anet.PlayerInput{PlayerID: a, Input: anet.Input{Seq: 1, Angle: angle, Fire: true}})

	got := ca.snapshotUntil(t, func(s anet.Snapshot) bool { return len(s.Bullets) > 0 })
	bl := got.Bullets[0]
	if bl.Owner != a || bl.ID == 0 || math.Hypot(bl.VX, bl.VY) == 0 {
		t.Fatalf("bullet = %+v, want moving bullet owned by %d", bl, a)
	}

	var h anet.Event
	hit := ca.snapshotUntil(t, func(s anet.Snapshot) bool {
		for _, e := range s.Events {
			if e.Type == anet.EventHit {
				h = e
				return true
			}
		}
		return false
	})
	if h.TargetID != b || h.ShooterID != a {
		t.Fatalf("hit = %+v, want %d -> %d", h, a, b)
	}
	if p, _ := findPlayer(hit, b); p.HP >= game.StartHP {
		t.Fatalf("target hp = %d, want < %d", p.HP, game.StartHP)
	}
}

// Короткое нажатие огня не должно теряться, если за тик пришло несколько вводов
// и последний уже с Fire=false.
func TestBufferInputKeepsFireWithinTick(t *testing.T) {
	rm := New("unit", slog.New(slog.DiscardHandler))
	rm.clients[1] = newFakeClient(1)

	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 1, Fire: true}})
	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 2, MoveX: 1}})
	if in := rm.pendingInputs[1]; in.Seq != 2 || in.MoveX != 1 || !in.Fire {
		t.Fatalf("pending = %+v, want seq 2, moveX 1, fire kept", in)
	}

	// Устаревший ввод не меняет ничего, в том числе Fire.
	clear(rm.pendingInputs)
	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 5}})
	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 4, Fire: true}})
	if in := rm.pendingInputs[1]; in.Seq != 5 || in.Fire {
		t.Fatalf("pending = %+v, stale input must be ignored", in)
	}
}

func TestBufferInputKeepsDashWithinTick(t *testing.T) {
	rm := New("unit", slog.New(slog.DiscardHandler))
	rm.clients[1] = newFakeClient(1)

	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 1, Dash: true}})
	rm.bufferInput(anet.PlayerInput{PlayerID: 1, Input: anet.Input{Seq: 2, MoveY: 1}})
	if in := rm.pendingInputs[1]; in.Seq != 2 || in.MoveY != 1 || !in.Dash {
		t.Fatalf("pending = %+v, want seq 2, moveY 1, dash kept", in)
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

func TestSnapshotRoundJSONShape(t *testing.T) {
	tests := []struct {
		name  string
		round game.RoundState
		want  string
	}{
		{
			"play carries only phase and timeLeft",
			game.RoundState{Phase: game.PhasePlay, TimeLeft: 42.5},
			`{"phase":"play","timeLeft":42.5}`,
		},
		{
			"intermission carries winner and scores",
			game.RoundState{Phase: game.PhaseIntermission, TimeLeft: 8, WinnerID: 2,
				Scores: []game.ScoreEntry{{ID: 1, Score: 3}, {ID: 2, Score: 10}}},
			`{"phase":"intermission","timeLeft":8,"winnerId":2,"scores":[{"id":1,"score":3},{"id":2,"score":10}]}`,
		},
		{
			"draw keeps winnerId 0",
			game.RoundState{Phase: game.PhaseIntermission, TimeLeft: 8,
				Scores: []game.ScoreEntry{{ID: 1, Score: 0}}},
			`{"phase":"intermission","timeLeft":8,"winnerId":0,"scores":[{"id":1,"score":0}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := New("test", slog.New(slog.DiscardHandler))
			c := newFakeClient(1)
			rm.clients[1] = c
			rm.broadcast(game.Snapshot{Round: tt.round})

			var raw struct {
				Round json.RawMessage `json:"round"`
			}
			if err := json.Unmarshal(<-c.msgs, &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw.Round); got != tt.want {
				t.Fatalf("round JSON =\n %s\nwant\n %s", got, tt.want)
			}
		})
	}
}

func TestSnapshotPickupsAndBoostedJSON(t *testing.T) {
	rm := New("test", slog.New(slog.DiscardHandler))
	c := newFakeClient(2)
	rm.clients[1] = c

	rm.broadcast(game.Snapshot{
		Players: []game.PlayerState{{ID: 1, HP: 100, Ammo: 7, Boosted: true}},
		Pickups: []game.Pickup{{ID: 7, Type: game.PickupAmmo, Pos: game.Vec2{X: 10, Y: 20}}},
	})
	var got struct {
		Players []map[string]any `json:"players"`
		Pickups json.RawMessage  `json:"pickups"`
	}
	if err := json.Unmarshal(<-c.msgs, &got); err != nil {
		t.Fatal(err)
	}
	if want := `[{"id":7,"type":"ammo","x":10,"y":20}]`; string(got.Pickups) != want {
		t.Fatalf("pickups = %s, want %s", got.Pickups, want)
	}
	if b, ok := got.Players[0]["boosted"]; !ok || b != true {
		t.Fatalf("player boosted = %v (present=%v), want true", b, ok)
	}
	if a, ok := got.Players[0]["ammo"]; !ok || a != float64(7) {
		t.Fatalf("player ammo = %v (present=%v), want 7", a, ok)
	}
	// Живой игрок: dead/respawnIn/invuln присутствуют и с нулевыми значениями.
	for k, want := range map[string]any{"dead": false, "respawnIn": float64(0), "invuln": false} {
		if v, ok := got.Players[0][k]; !ok || v != want {
			t.Fatalf("player %s = %v (present=%v), want %v", k, v, ok, want)
		}
	}
	if _, ok := got.Players[0]["speedUntil"]; ok {
		t.Fatal("speedUntil must not be sent")
	}

	// Без пикапов — пустой массив, не null.
	rm.broadcast(game.Snapshot{})
	var empty struct {
		Pickups json.RawMessage `json:"pickups"`
	}
	if err := json.Unmarshal(<-c.msgs, &empty); err != nil {
		t.Fatal(err)
	}
	if string(empty.Pickups) != "[]" {
		t.Fatalf("empty pickups = %s, want []", empty.Pickups)
	}
}

// Событие приходит клиенту ровно в одном снапшоте: мир сбрасывает события
// в начале каждого тика, а комната рассылает снапшот после каждого тика.
// Сколько выстрелов успеет случиться до отпускания огня, зависит от
// планировщика, поэтому проверяется точное соответствие: одно событие shot
// на каждую пулю (пуля видна уже в снапшоте тика своего выстрела).
func TestShotEventDeliveredOncePerShot(t *testing.T) {
	rm, _ := startRoom(t)
	c := newFakeClient(1024)
	id := join(t, rm, c)
	c.snapshotUntil(t, func(s anet.Snapshot) bool { return hasPlayer(s, id) })

	shots, bullets := 0, map[uint32]bool{}
	record := func(s anet.Snapshot) {
		for _, e := range s.Events {
			if e.Type == anet.EventShot && e.PlayerID == id {
				shots++
			}
		}
		for _, b := range s.Bullets {
			if b.Owner == id {
				bullets[b.ID] = true
			}
		}
	}

	rm.PushInput(anet.PlayerInput{PlayerID: id, Input: anet.Input{Seq: 1, Fire: true}})
	record(c.snapshotUntil(t, func(s anet.Snapshot) bool { return len(s.Events) > 0 }))
	rm.PushInput(anet.PlayerInput{PlayerID: id, Input: anet.Input{Seq: 2}}) // огонь отпущен
	n := 0
	c.snapshotUntil(t, func(s anet.Snapshot) bool { record(s); n++; return n == 10 })

	if shots == 0 || shots != len(bullets) {
		t.Fatalf("shot events = %d, distinct bullets = %d; want one event per shot", shots, len(bullets))
	}
}

func TestSnapshotDeadPlayerJSON(t *testing.T) {
	rm := New("test", slog.New(slog.DiscardHandler))
	c := newFakeClient(1)
	rm.clients[1] = c

	rm.broadcast(game.Snapshot{
		Players: []game.PlayerState{{ID: 2, Dead: true, RespawnIn: 2.5}, {ID: 3, HP: 100, Invuln: true}},
	})
	var got struct {
		Players []map[string]any `json:"players"`
	}
	if err := json.Unmarshal(<-c.msgs, &got); err != nil {
		t.Fatal(err)
	}
	dead, fresh := got.Players[0], got.Players[1]
	if dead["dead"] != true || dead["respawnIn"] != 2.5 || dead["invuln"] != false {
		t.Fatalf("мёртвый игрок = %v, want dead=true respawnIn=2.5 invuln=false", dead)
	}
	if fresh["dead"] != false || fresh["respawnIn"] != float64(0) || fresh["invuln"] != true {
		t.Fatalf("неуязвимый игрок = %v, want dead=false respawnIn=0 invuln=true", fresh)
	}
}
