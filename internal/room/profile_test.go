package room

import (
	"context"
	"strings"
	"testing"
	"time"

	"arena/internal/game"
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
