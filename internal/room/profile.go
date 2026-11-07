package room

import (
	"crypto/rand"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"arena/internal/game"
)

const (
	minNameLen = 2
	maxNameLen = 16
	maxKeyLen  = 64
)

// profile — данные игрока, которые знает только комната (не симуляция).
type profile struct {
	name string
	key  string // playerKey для таблицы лидеров; в снапшоты не попадает
}

// playerName — имя из join после проверки: пробелы по краям обрезаются,
// допустимы 2–16 символов (рун) без управляющих и непечатаемых.
// Некорректное имя заменяется на Player_<id>.
func playerName(raw string, id game.PlayerID) string {
	s := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(s); n >= minNameLen && n <= maxNameLen && printable(s) {
		return s
	}
	return "Player_" + strconv.FormatUint(uint64(id), 10)
}

// playerKey — ключ из join, если он не пуст, не длиннее 64 символов и без
// непечатаемых; иначе новый случайный.
func playerKey(raw string) string {
	if raw != "" && utf8.RuneCountInString(raw) <= maxKeyLen && printable(raw) {
		return raw
	}
	return rand.Text() // 26 символов base32, 128 бит из crypto/rand
}

// printable — валидный UTF-8 только из печатаемых символов (пробел допустим).
func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
