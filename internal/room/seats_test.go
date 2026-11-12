package room

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"arena/internal/game"
	anet "arena/internal/net"
)

func startRoomOpts(t *testing.T, opts ...Option) *Room {
	t.Helper()
	rm := New("test", nil, slog.New(slog.DiscardHandler), opts...)
	ctx, cancel := context.WithCancel(context.Background())
	go rm.Run(ctx)
	t.Cleanup(func() {
		cancel()
		<-rm.Done()
	})
	return rm
}

func TestMaxPlayersOptionLimitsJoins(t *testing.T) {
	rm := startRoomOpts(t, WithMaxPlayers(2))
	join(t, rm, newFakeClient(1024))
	join(t, rm, newFakeClient(1024))
	if _, err := rm.Join(context.Background(), newFakeClient(1024), anet.Join{}); !errors.Is(err, ErrFull) {
		t.Fatalf("третий вход в комнату на 2: err = %v, want ErrFull", err)
	}
	if got := rm.Players(); got != 2 {
		t.Fatalf("Players() = %d, want 2", got)
	}
	if got := New("x", nil, slog.New(slog.DiscardHandler), WithMaxPlayers(100)).MaxPlayers(); got != MaxPlayers {
		t.Fatalf("WithMaxPlayers(100) → %d, want %d", got, MaxPlayers)
	}
}

func TestReserveHoldsSeat(t *testing.T) {
	rm := startRoomOpts(t, WithMaxPlayers(2))
	if err := rm.Reserve(); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reserve(); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reserve(); !errors.Is(err, ErrFull) {
		t.Fatalf("резерв сверх вместимости: err = %v, want ErrFull", err)
	}
	if _, err := rm.Join(context.Background(), newFakeClient(1024), anet.Join{}); !errors.Is(err, ErrFull) {
		t.Fatalf("Join при занятых резервах: err = %v, want ErrFull", err)
	}
	rm.Unreserve()
	if _, err := rm.JoinReserved(context.Background(), newFakeClient(1024), anet.Join{}); err != nil {
		t.Fatalf("JoinReserved: %v", err)
	}
	if rm.Seats() != 1 || rm.Players() != 1 {
		t.Fatalf("seats=%d players=%d, want 1/1", rm.Seats(), rm.Players())
	}
}

func TestFailedJoinReleasesSeat(t *testing.T) {
	rm := New("test", nil, slog.New(slog.DiscardHandler)) // Run не запущен: Join ждёт
	if err := rm.Reserve(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := rm.JoinReserved(ctx, newFakeClient(1), anet.Join{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if rm.Seats() != 0 {
		t.Fatalf("Seats() = %d после неудачного входа, want 0", rm.Seats())
	}
}

func TestLeaveReleasesSeat(t *testing.T) {
	rm := startRoomOpts(t)
	id := join(t, rm, newFakeClient(1024))
	rm.Leave(id)
	deadline := time.Now().Add(waitTimeout)
	for rm.Seats() != 0 || rm.Players() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("seats=%d players=%d после выхода, want 0/0", rm.Seats(), rm.Players())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRetire(t *testing.T) {
	rm := startRoomOpts(t)
	if err := rm.Reserve(); err != nil {
		t.Fatal(err)
	}
	if rm.Retire() {
		t.Fatal("Retire списал комнату с резервом")
	}
	rm.Unreserve()
	if !rm.Retire() {
		t.Fatal("Retire не списал пустую комнату")
	}
	if err := rm.Reserve(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Reserve после Retire: err = %v, want ErrClosed", err)
	}
	rm.Unreserve() // лишнее освобождение не «оживляет» комнату
	if err := rm.Reserve(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Reserve после Retire+Unreserve: err = %v, want ErrClosed", err)
	}
	if rm.Seats() != 0 {
		t.Fatalf("Seats() у списанной = %d, want 0", rm.Seats())
	}
}

func TestWelcomeHasMode(t *testing.T) {
	rm := startRoomOpts(t, WithMode("zombie"))
	c := newFakeClient(1024)
	join(t, rm, c)
	if w := c.welcome(t); w.Mode != "zombie" {
		t.Fatalf("welcome.mode = %q, want zombie", w.Mode)
	}
	if rm.Mode() != "zombie" {
		t.Fatalf("Mode() = %q", rm.Mode())
	}
	if New("x", nil, slog.New(slog.DiscardHandler)).Mode() != DefaultMode {
		t.Fatal("режим по умолчанию не DefaultMode")
	}
}

func TestPhasePublished(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.RoundTime = 0.1 // раунд кончается по времени через 2 тика
	cfg.IntermissionTime = 10
	rm := startRoomOpts(t, WithGameConfig(cfg))
	if rm.Phase() != game.PhasePlay {
		t.Fatalf("начальная фаза %q", rm.Phase())
	}
	deadline := time.Now().Add(waitTimeout)
	for rm.Phase() != game.PhaseIntermission {
		if time.Now().After(deadline) {
			t.Fatal("фаза intermission не опубликована")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTickObserver(t *testing.T) {
	ticks := make(chan time.Duration, 4)
	startRoomOpts(t, WithTickObserver(func(d time.Duration) {
		select {
		case ticks <- d:
		default:
		}
	}))
	select {
	case d := <-ticks:
		if d < 0 {
			t.Fatalf("длительность тика %v < 0", d)
		}
	case <-time.After(waitTimeout):
		t.Fatal("наблюдатель тика не вызван")
	}
}
