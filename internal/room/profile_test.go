package room

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"arena/internal/game"
	"arena/internal/leaderboard"
	anet "arena/internal/net"
)

func TestPlayerNameValidation(t *testing.T) {
	tests := []struct {
		raw, want string
	}{
		{"Alice", "Alice"},
		{"  Bob  ", "Bob"},                       // пробелы по краям обрезаются
		{"Ab", "Ab"},                             // ровно 2
		{"abcdefghijklmnop", "abcdefghijklmnop"}, // ровно 16
		{"Вася Пупкин", "Вася Пупкин"},           // длина в символах, не в байтах
		{"Игрок_шестнадцат", "Игрок_шестнадцат"}, // 16 кириллических = 32 байта
		{"", "Player_7"},
		{"A", "Player_7"},
		{"   A   ", "Player_7"},           // после обрезки 1 символ
		{"abcdefghijklmnopq", "Player_7"}, // 17
		{"bad\nname", "Player_7"},
		{"tab\there", "Player_7"},
		{"zero" + string(rune(0x200b)) + "width", "Player_7"}, // непечатаемый
		{"\xff\xfe", "Player_7"},                              // невалидный UTF-8
	}
	for _, tt := range tests {
		if got := playerName(tt.raw, 7); got != tt.want {
			t.Errorf("playerName(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestPlayerKeyValidation(t *testing.T) {
	for _, k := range []string{"k", "550e8400-e29b-41d4-a716-446655440000", strings.Repeat("я", maxKeyLen)} {
		if got := playerKey(k); got != k {
			t.Errorf("playerKey(%q) = %q, корректный ключ должен сохраняться", k, got)
		}
	}
	seen := map[string]bool{}
	for _, k := range []string{"", strings.Repeat("a", maxKeyLen+1), "bad\x00key"} {
		got := playerKey(k)
		if got == k || got == "" || len(got) > maxKeyLen || !printable(got) {
			t.Errorf("playerKey(%q) = %q, want новый корректный ключ", k, got)
		}
		if seen[got] {
			t.Errorf("сгенерированный ключ %q повторился", got)
		}
		seen[got] = true
	}
}

// joinProfile — вход в комнату с данным join; возвращает Welcome.
func joinProfile(t *testing.T, rm *Room, j anet.Join) (uint32, anet.Welcome) {
	t.Helper()
	c := newFakeClient(1024)
	id, err := rm.Join(context.Background(), c, j)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	return id, c.welcome(t)
}

// Каждый вход получает наименее занятый цвет: 8 игроков — 8 разных цветов
// по порядку; цвет ушедшего освобождается и достаётся следующему.
func TestColorsLeastUsedAndFreedOnLeave(t *testing.T) {
	rm, _ := startRoom(t)
	ids := make([]uint32, MaxPlayers)
	for i := range MaxPlayers {
		var w anet.Welcome
		ids[i], w = joinProfile(t, rm, anet.Join{})
		if w.Color != i {
			t.Fatalf("игрок %d: color = %d, want %d", i, w.Color, i)
		}
	}

	rm.Leave(ids[3])
	rm.Leave(ids[5])
	for _, want := range []int{3, 5} {
		var w anet.Welcome
		deadline := time.Now().Add(waitTimeout)
		for { // Leave асинхронный: ждём, пока слот освободится
			c := newFakeClient(1024)
			if _, err := rm.Join(context.Background(), c, anet.Join{}); err == nil {
				w = c.welcome(t)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("слот не освободился")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if w.Color != want {
			t.Fatalf("после выхода: color = %d, want %d", w.Color, want)
		}
	}
}

// При переполнении палитры (больше игроков, чем цветов) выбирается наименее
// занятый, а при равенстве — меньший индекс.
func TestPickColorLeastUsed(t *testing.T) {
	clients := map[game.PlayerID]*client{}
	add := func(id game.PlayerID, color int) {
		clients[id] = &client{profile: profile{color: color}}
	}
	if got := pickColor(clients); got != 0 {
		t.Fatalf("пустая комната: %d, want 0", got)
	}
	for i := range Colors {
		add(game.PlayerID(i+1), i)
	}
	add(100, 0)
	add(101, 2)
	if got := pickColor(clients); got != 1 {
		t.Fatalf("цвета 0 и 2 заняты дважды: %d, want 1", got)
	}
}

// recStore — Store, запоминающий вызовы AddScore.
type recStore struct {
	mu    sync.Mutex
	calls []scoreUpdate
	added chan struct{}
	block chan struct{} // не nil — AddScore ждёт его закрытия
}

func newRecStore() *recStore { return &recStore{added: make(chan struct{}, 64)} }

func (s *recStore) AddScore(ctx context.Context, key, name string, score int) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, scoreUpdate{key: key, name: name, score: score})
	s.mu.Unlock()
	select { // сигнал «что-то записано»; переполнение не важно
	case s.added <- struct{}{}:
	default:
	}
	return nil
}

func (s *recStore) Top(context.Context, int) ([]leaderboard.Entry, error) { return nil, nil }

func (s *recStore) snapshot() []scoreUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]scoreUpdate(nil), s.calls...)
}

// Фраг в настоящем тике комнаты записывается стрелку (ключ и имя из join)
// +1 очко; убитому — ничего.
func TestKillRecordedToLeaderboard(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Map = game.Map{Name: "open", Width: 800, Height: 600}
	cfg.BulletDamage = game.StartHP // одно попадание — фраг
	cfg.InvulnTime = 0
	st := newRecStore()
	rm := New("test", st, slog.New(slog.DiscardHandler))
	rm.world = game.NewWorld(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go rm.Run(ctx)
	t.Cleanup(func() { cancel(); <-rm.Done() })

	ca, cb := newFakeClient(1024), newFakeClient(1024)
	a, err := rm.Join(context.Background(), ca, anet.Join{Name: "Shooter", PlayerKey: "key-a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := rm.Join(context.Background(), cb, anet.Join{Name: "Victim", PlayerKey: "key-b"})
	if err != nil {
		t.Fatal(err)
	}
	s := ca.snapshotUntil(t, func(s anet.Snapshot) bool { return hasPlayer(s, a) && hasPlayer(s, b) })
	pa, _ := findPlayer(s, a)
	pb, _ := findPlayer(s, b)
	angle := math.Atan2(pb.Y-pa.Y, pb.X-pa.X)
	rm.PushInput(anet.PlayerInput{PlayerID: a, Input: anet.Input{Seq: 1, Angle: angle, Fire: true}})

	select {
	case <-st.added:
	case <-time.After(waitTimeout):
		t.Fatal("фраг не записан в таблицу лидеров")
	}
	rm.PushInput(anet.PlayerInput{PlayerID: a, Input: anet.Input{Seq: 2, Angle: angle}})
	if got := st.snapshot()[0]; got != (scoreUpdate{key: "key-a", name: "Shooter", score: 1}) {
		t.Fatalf("AddScore = %+v, want key-a / Shooter / 1", got)
	}
}

// Зависшее хранилище не блокирует тик: записи сверх очереди теряются,
// recordKills возвращается сразу.
func TestRecordKillsDoesNotBlock(t *testing.T) {
	st := newRecStore()
	st.block = make(chan struct{})
	rm := New("test", st, slog.New(slog.DiscardHandler))
	rm.clients[1] = &client{conn: newFakeClient(1), profile: profile{name: "A", key: "k"}}
	writerDone := make(chan struct{})
	go rm.writeScores(writerDone)

	kills := []game.Event{{Type: game.EventKill, Shooter: 1, Target: 2}, {Type: game.EventHit, Shooter: 1}}
	done := make(chan struct{})
	go func() {
		for range scoresBuffer * 2 {
			rm.recordKills(kills)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("recordKills заблокировался на зависшем хранилище")
	}
	close(st.block)
	close(rm.scores)
	<-writerDone
	// Записалось не больше, чем влезло в очередь (+1 — та, что ждала в AddScore).
	if n := len(st.snapshot()); n == 0 || n > scoresBuffer+1 {
		t.Fatalf("записано %d, want 1…%d", n, scoresBuffer+1)
	}
	for _, c := range st.snapshot() {
		if c != (scoreUpdate{key: "k", name: "A", score: 1}) {
			t.Fatalf("лишняя запись %+v: hit не должен записываться", c)
		}
	}
}

// Стрелок, отключившийся до тика с фрагом, не записывается; без хранилища
// recordKills ничего не делает.
func TestRecordKillsSkipsUnknownShooterAndNilStore(t *testing.T) {
	st := newRecStore()
	rm := New("test", st, slog.New(slog.DiscardHandler))
	rm.recordKills([]game.Event{{Type: game.EventKill, Shooter: 9, Target: 2}})
	if len(rm.scores) != 0 {
		t.Fatal("фраг отключившегося стрелка поставлен в очередь")
	}

	rm = New("test", nil, slog.New(slog.DiscardHandler))
	rm.clients[1] = &client{conn: newFakeClient(1), profile: profile{key: "k"}}
	rm.recordKills([]game.Event{{Type: game.EventKill, Shooter: 1, Target: 2}})
	if len(rm.scores) != 0 {
		t.Fatal("без хранилища фраг поставлен в очередь")
	}
}
