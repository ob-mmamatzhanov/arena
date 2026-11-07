package room

import (
	"strings"
	"testing"
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
