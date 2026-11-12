package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	anet "arena/internal/net"
	"arena/internal/room"
)

const waitTimeout = 3 * time.Second

// fakeClock — инжектируемые часы; безопасны для параллельного чтения.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *fakeClock) Now() time.Time            { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) Advance(d time.Duration)   { c.ns.Add(int64(d)) }
func (c *fakeClock) Set(t time.Time) time.Time { c.ns.Store(t.UnixNano()); return t }

// testManager — менеджер без фонового reapLoop: удаление вызывается явно
// через reap, время — из fakeClock.
func testManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	m := newManager(Config{Now: clk.Now})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return m, clk
}

// nopClient — room.Client, который принимает всё и никуда не пишет.
type nopClient struct{}

func (nopClient) TrySend([]byte) bool                { return true }
func (nopClient) Close(websocket.StatusCode, string) {}

func joinRoom(t *testing.T, rm *room.Room) uint32 {
	t.Helper()
	id, err := rm.Join(context.Background(), nopClient{}, anet.Join{})
	if err != nil {
		t.Fatalf("join %s: %v", rm.Name(), err)
	}
	return id
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCreateValidates(t *testing.T) {
	m, _ := testManager(t)
	for _, tt := range []struct {
		opts Opts
		err  error
	}{
		{Opts{Mode: "ctf", MaxPlayers: 4}, ErrBadMode},
		{Opts{Mode: "", MaxPlayers: 4}, ErrBadMode},
		{Opts{Mode: ModeArena, MaxPlayers: 1}, ErrBadMaxPlayers},
		{Opts{Mode: ModeArena, MaxPlayers: 9}, ErrBadMaxPlayers},
	} {
		if _, err := m.Create(tt.opts); !errors.Is(err, tt.err) {
			t.Errorf("Create(%+v): err = %v, want %v", tt.opts, err, tt.err)
		}
	}
	rm, err := m.Create(Opts{Mode: ModeZombie, MaxPlayers: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rm.Mode() != ModeZombie || rm.MaxPlayers() != 3 {
		t.Fatalf("комната %s: mode=%q maxPlayers=%d", rm.Name(), rm.Mode(), rm.MaxPlayers())
	}
	if len(rm.Name()) != idLen {
		t.Fatalf("id %q: длина %d, want %d", rm.Name(), len(rm.Name()), idLen)
	}
	if m.Get(rm.Name()) != rm || m.Get("nope") != nil {
		t.Fatal("Get вернул не ту комнату")
	}
}

func TestRoomLimit(t *testing.T) {
	m, _ := testManager(t)
	for i := range MaxRooms {
		if _, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 2, Private: i%2 == 0}); err != nil {
			t.Fatalf("комната #%d: %v", i+1, err)
		}
	}
	if _, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 2}); !errors.Is(err, ErrTooManyRooms) {
		t.Fatalf("комната #%d: err = %v, want ErrTooManyRooms", MaxRooms+1, err)
	}
	// Все публичные arena заняты резервами — автозаполнению некуда и нельзя создать новую.
	for range MaxRooms {
		m.FindOrCreate(ModeArena)
	}
	if rm := m.FindOrCreate(ModeArena); rm != nil {
		t.Fatalf("FindOrCreate сверх лимита вернул %s", rm.Name())
	}
}

func TestFindOrCreateSkipsFullRoom(t *testing.T) {
	m, _ := testManager(t)
	small, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 2})
	if err != nil {
		t.Fatal(err)
	}
	joinRoom(t, small)
	if got := m.FindOrCreate(ModeArena); got != small {
		t.Fatalf("FindOrCreate = %v, want комнату %s со свободным местом", got, small.Name())
	}
	// Последнее место занято резервом: комната полна.
	next := m.FindOrCreate(ModeArena)
	if next == nil || next == small {
		t.Fatalf("FindOrCreate положил в полную комнату %s", small.Name())
	}
	if small.Seats() != 2 {
		t.Fatalf("seats = %d, want 2", small.Seats())
	}
	// Резерв уходит в JoinReserved — новый игрок в той же комнате.
	if _, err := next.JoinReserved(context.Background(), nopClient{}, anet.Join{}); err != nil {
		t.Fatal(err)
	}
	if next.Players() != 1 {
		t.Fatalf("players = %d, want 1", next.Players())
	}
}

func TestFindOrCreateByMode(t *testing.T) {
	m, _ := testManager(t)
	a := m.FindOrCreate(ModeArena)
	z := m.FindOrCreate(ModeZombie)
	if a == nil || z == nil || a == z {
		t.Fatalf("arena=%v zombie=%v: режимы должны жить в разных комнатах", a, z)
	}
	if z.Mode() != ModeZombie {
		t.Fatalf("mode = %q", z.Mode())
	}
	if m.FindOrCreate(ModeZombie) != z || m.FindOrCreate(ModeArena) != a {
		t.Fatal("автозаполнение не вернуло существующую комнату режима")
	}
	if m.FindOrCreate("ctf") != nil {
		t.Fatal("FindOrCreate принял неизвестный режим")
	}
}

func TestPrivateRoomHiddenFromListAndAutofill(t *testing.T) {
	m, _ := testManager(t)
	priv, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8})
	if err != nil {
		t.Fatal(err)
	}

	list := m.List()
	if len(list) != 1 || list[0].ID != pub.Name() {
		t.Fatalf("List() = %+v, want только %s", list, pub.Name())
	}
	if l := list[0]; l.Mode != ModeArena || l.Phase != "play" || l.Players != 0 || l.MaxPlayers != 8 {
		t.Fatalf("RoomInfo = %+v", l)
	}
	for range 20 {
		if rm := m.FindOrCreate(ModeArena); rm == priv {
			t.Fatal("автозаполнение положило игрока в приватную комнату")
		}
	}
	if priv.Seats() != 0 {
		t.Fatalf("в приватной комнате заняты места: %d", priv.Seats())
	}

	code := m.Code(priv.Name())
	if len(code) != codeLen || strings.Trim(code, codeAlphabet) != "" {
		t.Fatalf("код %q: нужны %d букв из %q", code, codeLen, codeAlphabet)
	}
	if id, ok := m.ByCode(strings.ToLower(code)); !ok || id != priv.Name() {
		t.Fatalf("ByCode(%q) = %q, %v", code, id, ok)
	}
	if m.Code(pub.Name()) != "" {
		t.Fatal("у публичной комнаты есть код")
	}
}

func TestPrivateCodeUnique(t *testing.T) {
	m, _ := testManager(t)
	seq := []string{"ABCD", "ABCD", "ABCD", "WXYZ"}
	var i int
	m.newCode = func() string { s := seq[min(i, len(seq)-1)]; i++; return s }

	first, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if c1, c2 := m.Code(first.Name()), m.Code(second.Name()); c1 != "ABCD" || c2 != "WXYZ" {
		t.Fatalf("коды %q и %q, want ABCD и WXYZ", c1, c2)
	}

	// Генератор, выдающий только занятый код, — ошибка, а не дубль.
	m.newCode = func() string { return "ABCD" }
	if _, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8, Private: true}); err == nil {
		t.Fatal("создана комната с неуникальным кодом")
	}

	// Случайные коды: без неоднозначных букв.
	for range 1000 {
		if c := randomCode(); strings.ContainsAny(c, "OI01") || len(c) != codeLen {
			t.Fatalf("плохой код %q", c)
		}
	}
}

func TestEmptyRoomRemovedAfterTTL(t *testing.T) {
	m, clk := testManager(t)
	rm, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	code := m.Code(rm.Name())

	clk.Advance(EmptyTTL - time.Second)
	m.reap()
	if m.Get(rm.Name()) == nil {
		t.Fatal("комната удалена раньше EmptyTTL")
	}
	clk.Advance(time.Second)
	m.reap()
	if m.Get(rm.Name()) != nil {
		t.Fatal("пустая комната не удалена через EmptyTTL")
	}
	if _, ok := m.ByCode(code); ok {
		t.Fatal("код удалённой комнаты остался в реестре")
	}
	select {
	case <-rm.Done():
	case <-time.After(waitTimeout):
		t.Fatal("удалённая комната не остановлена")
	}
	if err := rm.Reserve(); !errors.Is(err, room.ErrClosed) {
		t.Fatalf("Reserve в удалённой комнате: err = %v, want ErrClosed", err)
	}
}

func TestOccupiedRoomNotRemoved(t *testing.T) {
	m, clk := testManager(t)
	rm, err := m.Create(Opts{Mode: ModeArena, MaxPlayers: 8})
	if err != nil {
		t.Fatal(err)
	}
	id := joinRoom(t, rm)
	clk.Advance(time.Hour)
	m.reap()
	if m.Get(rm.Name()) == nil {
		t.Fatal("удалена комната с игроком")
	}

	// Резерв (игрок между FindOrCreate и входом) тоже держит комнату.
	rm.Leave(id)
	waitFor(t, "выход игрока", func() bool { return rm.Seats() == 0 })
	if m.FindOrCreate(ModeArena) != rm {
		t.Fatal("FindOrCreate не вернул существующую комнату")
	}
	clk.Advance(time.Hour)
	m.reap()
	if m.Get(rm.Name()) == nil {
		t.Fatal("удалена комната с резервом")
	}

	// Отсчёт EmptyTTL начинается с проверки, заставшей комнату пустой.
	rm.Unreserve()
	m.reap()
	clk.Advance(EmptyTTL - time.Second)
	m.reap()
	if m.Get(rm.Name()) == nil {
		t.Fatal("комната удалена раньше EmptyTTL после ухода игроков")
	}
	clk.Advance(time.Second)
	m.reap()
	if m.Get(rm.Name()) != nil {
		t.Fatal("комната не удалена через EmptyTTL после ухода игроков")
	}
}

func TestParallelFindOrCreate(t *testing.T) {
	m, _ := testManager(t)
	const n = 20
	got := make([]*room.Room, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			got[i] = m.FindOrCreate(ModeArena)
		})
	}
	close(start)
	wg.Wait()

	seats := map[*room.Room]int{}
	for i, rm := range got {
		if rm == nil {
			t.Fatalf("вызов #%d вернул nil", i)
		}
		seats[rm]++
	}
	want := (n + room.MaxPlayers - 1) / room.MaxPlayers
	if len(seats) != want || len(m.List()) != want {
		t.Fatalf("создано комнат: %d (в реестре %d), want %d", len(seats), len(m.List()), want)
	}
	for rm, k := range seats {
		if k > rm.MaxPlayers() || rm.Seats() != k {
			t.Fatalf("комната %s: выдано %d мест, seats=%d, max=%d", rm.Name(), k, rm.Seats(), rm.MaxPlayers())
		}
	}
}

func TestShutdown(t *testing.T) {
	m := newManager(Config{})
	rooms := []*room.Room{m.FindOrCreate(ModeArena), m.FindOrCreate(ModeZombie)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, rm := range rooms {
		select {
		case <-rm.Done():
		default:
			t.Fatalf("комната %s не остановлена", rm.Name())
		}
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("Done не закрыт после Shutdown")
	}
	if _, err := m.Create(Opts{Mode: ModeArena}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Create после Shutdown: err = %v, want ErrClosed", err)
	}
	if m.FindOrCreate(ModeArena) != nil || len(m.List()) != 0 {
		t.Fatal("после Shutdown остались комнаты")
	}
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("повторный Shutdown: %v", err)
	}
}

// Комнатам на 5–8 игроков (и автозаполнению — оно создаёт комнаты на 8)
// достаётся арена с дополнительными укрытиями, комнатам поменьше — обычная.
func TestGameConfigMapByCapacity(t *testing.T) {
	for n, want := range map[int]string{2: "arena", 4: "arena", 5: "arena-large", room.MaxPlayers: "arena-large"} {
		if got := gameConfig(ModeArena, n).Map.Name; got != want {
			t.Errorf("gameConfig(arena, %d).Map = %s, want %s", n, got, want)
		}
	}
}
