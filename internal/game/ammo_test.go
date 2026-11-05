package game

import (
	"math/rand/v2"
	"testing"
)

// ammoConfig — testConfig с включёнными патронами (значения как в DefaultConfig).
func ammoConfig() Config {
	cfg := testConfig()
	cfg.StartAmmo, cfg.MaxAmmo, cfg.AmmoAmount = 12, 30, 10
	return cfg
}

func countEvents(events []Event, typ EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestNewPlayerStartsWithAmmo(t *testing.T) {
	w := NewWorld(ammoConfig())
	w.AddPlayer(1)
	if got := playerState(t, w, 1).Ammo; got != 12 {
		t.Fatalf("ammo нового игрока = %d, want 12", got)
	}
}

func TestShotConsumesAmmo(t *testing.T) {
	w := NewWorld(ammoConfig())
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	w.players[1].Ammo = 2

	// Огонь зажат 20 тиков: кулдаун позволил бы 5 выстрелов, патронов — 2.
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: 1, Fire: true}})
	shots := countEvents(w.Snapshot().Events, EventShot)
	for range 19 {
		w.Update(dt, nil)
		shots += countEvents(w.Snapshot().Events, EventShot)
	}
	if shots != 2 {
		t.Fatalf("выстрелов = %d, want 2 (по числу патронов)", shots)
	}
	if got := playerState(t, w, 1).Ammo; got != 0 {
		t.Fatalf("ammo = %d, want 0", got)
	}
}

func TestCannotFireWithZeroAmmo(t *testing.T) {
	w := NewWorld(ammoConfig())
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	w.players[1].Ammo = 0
	w.players[1].Heat = 0.5

	for i := uint32(1); i <= 10; i++ {
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, Fire: true}})
		s := w.Snapshot()
		if len(s.Bullets) != 0 {
			t.Fatalf("тик %d: пуля без патронов: %+v", i, s.Bullets)
		}
		if n := countEvents(s.Events, EventShot); n != 0 {
			t.Fatalf("тик %d: событие shot без патронов", i)
		}
	}
	if got := playerState(t, w, 1); got.Ammo != 0 || got.Heat >= 0.5 {
		t.Fatalf("ammo = %d, heat = %v: без выстрелов патроны не уходят в минус, ствол остывает", got.Ammo, got.Heat)
	}
}

func TestAmmoPickupCapsAtMax(t *testing.T) {
	tests := []struct{ ammo, want int }{{0, 10}, {12, 22}, {25, 30}, {30, 30}}
	for _, tt := range tests {
		w := NewWorld(ammoConfig())
		w.AddPlayer(1)
		place(w, 1, 500, 500)
		w.players[1].Ammo = tt.ammo
		dropPickup(w, PickupAmmo, 505, 500)
		w.Update(dt, nil)
		if got := playerState(t, w, 1).Ammo; got != tt.want {
			t.Errorf("ammo %d + пикап = %d, want %d", tt.ammo, got, tt.want)
		}
		s := w.Snapshot()
		if len(s.Pickups) != 0 {
			t.Errorf("ammo %d: пикап не подобран", tt.ammo)
		}
		if len(s.Events) != 1 || s.Events[0].Type != EventPickup || s.Events[0].Pickup != PickupAmmo {
			t.Errorf("ammo %d: события = %+v, want один pickup ammo", tt.ammo, s.Events)
		}
	}
}

func TestPickupTypeForBoundaries(t *testing.T) {
	tests := []struct {
		roll int
		want PickupType
	}{
		{0, PickupHealth}, {59, PickupHealth},
		{60, PickupSpeed}, {99, PickupSpeed},
	}
	for _, tt := range tests {
		if got := pickupTypeFor(tt.roll); got != tt.want {
			t.Errorf("pickupTypeFor(%d) = %q, want %q", tt.roll, got, tt.want)
		}
	}
	sum := 0
	for _, pw := range pickupWeights {
		sum += pw.weight
	}
	if sum != pickupWeightTotal {
		t.Fatalf("сумма весов = %d, want %d", sum, pickupWeightTotal)
	}
}

// При фиксированном seed выбор воспроизводим и совпадает с ручным броском
// того же генератора, а частоты близки к 60/40; ammo в общий поток не попадает.
func TestPickupTypeWeightedByDeterministicRand(t *testing.T) {
	const n = 20000
	cfg := testConfig()
	cfg.Seed = 42
	w := NewWorld(cfg)
	ref := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)) // тот же генератор, что у мира

	counts := map[PickupType]int{}
	for i := range n {
		got := w.randomPickupType()
		if want := pickupTypeFor(ref.IntN(pickupWeightTotal)); got != want {
			t.Fatalf("бросок %d: %q, want %q (выбор должен идти только через rng мира)", i, got, want)
		}
		counts[got]++
	}
	for typ, want := range map[PickupType]float64{PickupAmmo: 0, PickupHealth: 0.6, PickupSpeed: 0.4} {
		if got := float64(counts[typ]) / n; got < want-0.02 || got > want+0.02 {
			t.Errorf("доля %q = %.3f, want %.2f±0.02", typ, got, want)
		}
	}
}

func TestAmmoRestoredOnRespawnAndNewRound(t *testing.T) {
	cfg := ammoConfig()
	cfg.RoundTime, cfg.KillLimit, cfg.IntermissionTime = 0, 1, 1
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	w.players[2].Ammo = 3

	killOnce(t, w) // фраг 1 → 2 достигает лимита: респаун и конец раунда
	if got := w.players[2].Ammo; got != 12 {
		t.Fatalf("ammo после респауна = %d, want 12", got)
	}
	if got := w.players[1].Ammo; got != 11 {
		t.Fatalf("ammo стрелка = %d, want 11", got)
	}
	tickN(w, 20) // intermission 1 с
	if w.phase != PhasePlay {
		t.Fatalf("setup: phase = %q", w.phase)
	}
	if got := w.players[1].Ammo; got != 12 {
		t.Fatalf("ammo после нового раунда = %d, want 12", got)
	}
}
