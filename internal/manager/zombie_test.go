package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	anet "arena/internal/net"
	"arena/internal/room"
)

// recClient — room.Client, складывающий сообщения в буфер (хватает на
// несколько сотен тиков без чтения).
type recClient struct{ msgs chan []byte }

func (c recClient) TrySend(b []byte) bool {
	select {
	case c.msgs <- b:
		return true
	default:
		return false
	}
}
func (recClient) Close(websocket.StatusCode, string) {}

func TestGameConfigEnablesZombiesOnlyForZombieMode(t *testing.T) {
	for _, n := range []int{2, room.MaxPlayers} {
		if z := gameConfig(ModeZombie, n); z.Zombie.WaveBase <= 0 || z.RoundTime != 0 || z.KillLimit != 0 {
			t.Errorf("zombie, %d игроков: Zombie %+v, RoundTime %v, KillLimit %d", n, z.Zombie, z.RoundTime, z.KillLimit)
		}
		if a := gameConfig(ModeArena, n); a.Zombie.WaveBase != 0 {
			t.Errorf("arena, %d игроков: включены зомби %+v", n, a.Zombie)
		}
	}
}

// Комната режима zombie, созданная менеджером, с одним игроком: через 6 с
// симуляции (120 тиков после входа) в снапшоте 2 зомби первой волны.
// Тики ускорены (WithTickInterval), шаг симуляции — обычные 50 мс.
func TestZombieRoomSpawnsFirstWave(t *testing.T) {
	m, _ := testManager(t)
	m.roomOpts = []room.Option{room.WithTickInterval(time.Millisecond)}
	rm, err := m.Create(Opts{Mode: ModeZombie, MaxPlayers: 4})
	if err != nil {
		t.Fatal(err)
	}
	c := recClient{msgs: make(chan []byte, 1024)}
	id, err := rm.Join(context.Background(), c, anet.Join{Type: anet.TypeJoin, Name: "Solo"})
	if err != nil {
		t.Fatal(err)
	}

	var w anet.Welcome
	if err := json.Unmarshal(<-c.msgs, &w); err != nil || w.Mode != ModeZombie || w.ZombieRadius != 12 {
		t.Fatalf("welcome: %+v, %v", w, err)
	}

	const after = 120 // 6 с при dt 0.05
	var first uint64
	deadline := time.After(waitTimeout)
	for {
		var raw []byte
		select {
		case raw = <-c.msgs:
		case <-deadline:
			t.Fatal("не дождались снапшота через 6 с симуляции")
		}
		var s anet.Snapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if first == 0 {
			first = s.Tick // первый снапшот — тик входа игрока
		}
		if s.Tick < first+after {
			continue
		}
		if len(s.Zombies) != 2 || s.Round.Wave == nil || *s.Round.Wave != 1 ||
			s.Round.ZombiesLeft == nil || *s.Round.ZombiesLeft != 2 {
			t.Fatalf("тик %d (+%d): зомби %d, round %+v", s.Tick, s.Tick-first, len(s.Zombies), s.Round)
		}
		for _, z := range s.Zombies {
			if z.HP != 30 || z.Speed != 90 {
				t.Fatalf("зомби %+v, want hp 30, speed 90", z)
			}
		}
		if len(s.Players) != 1 || s.Players[0].ID != id || s.Players[0].Dead {
			t.Fatalf("игрок: %+v", s.Players)
		}
		return
	}
}
