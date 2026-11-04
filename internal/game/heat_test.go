package game

import (
	"math"
	"testing"
)

// shotAngle — направление последней выпущенной пули.
func shotAngle(t *testing.T, w *World) float64 {
	t.Helper()
	bs := w.Snapshot().Bullets
	if len(bs) == 0 {
		t.Fatal("no bullets")
	}
	v := bs[len(bs)-1].Vel
	return math.Atan2(v.Y, v.X)
}

func TestHeatGrowsWhileFiring(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)

	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Fire: true}})
	if h := playerState(t, w, 1).Heat; !approx(h, cfg.HeatPerShot) {
		t.Fatalf("heat after first shot = %v, want %v", h, cfg.HeatPerShot)
	}

	// Огонь зажат: выстрел раз в 4 тика (+0.2), между ними остывание 3×0.025.
	// Чистый прирост +0.125 за цикл — heat растёт и упирается в 1.
	prev, peak := playerState(t, w, 1).Heat, 0.0
	for cycle := range 12 {
		tickN(w, 4)
		h := playerState(t, w, 1).Heat
		if h > 1+eps {
			t.Fatalf("heat %v exceeds 1", h)
		}
		if cycle < 5 && h <= prev {
			t.Fatalf("cycle %d: heat did not grow: %v -> %v", cycle, prev, h)
		}
		prev, peak = h, max(peak, h)
	}
	if !approx(peak, 1) {
		t.Fatalf("heat peak while holding fire = %v, want capped at 1", peak)
	}
}

func TestHeatDecaysAtRest(t *testing.T) {
	w := newWorldWith(t, 1)
	w.players[1].Heat = 1
	tickN(w, 20) // 1 с без стрельбы
	if h := playerState(t, w, 1).Heat; !approx(h, 0.5) {
		t.Fatalf("heat after 1 s at rest = %v, want 0.5", h)
	}
	tickN(w, 20)
	if h := playerState(t, w, 1).Heat; h != 0 {
		t.Fatalf("heat after 2 s at rest = %v, want 0", h)
	}
	tickN(w, 20)
	if h := playerState(t, w, 1).Heat; h != 0 {
		t.Fatalf("heat went negative: %v", h)
	}
}

func TestNoSpreadAtZeroHeat(t *testing.T) {
	for seed := uint64(1); seed <= 50; seed++ {
		cfg := testConfig()
		cfg.Seed = seed
		w := NewWorld(cfg)
		w.AddPlayer(1)
		angle := 0.1 * float64(seed)
		w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: angle, Fire: true}})
		v := w.Snapshot().Bullets[0].Vel
		a := playerState(t, w, 1).Angle // нормализованный сервером угол прицела
		if want := (Vec2{X: math.Cos(a) * cfg.BulletSpeed, Y: math.Sin(a) * cfg.BulletSpeed}); v != want {
			t.Fatalf("seed %d: cold shot vel = %v, want exactly %v", seed, v, want)
		}
	}
}

func TestSpreadBoundedByHeat(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	const aim = 0.7
	maxDev, nonZero := 0.0, false
	for i := uint32(1); i <= 200; i++ {
		w.players[1].Heat, w.players[1].cooldown = 1, 0
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, Angle: aim, Fire: true}})
		dev := math.Abs(math.Remainder(shotAngle(t, w)-aim, 2*math.Pi))
		maxDev = max(maxDev, dev)
		nonZero = nonZero || dev > 1e-6
	}
	if maxDev > cfg.MaxSpread+eps {
		t.Fatalf("deviation %v exceeds ±%v at heat=1", maxDev, cfg.MaxSpread)
	}
	if !nonZero || maxDev < cfg.MaxSpread/2 {
		t.Fatalf("spread at heat=1 suspiciously small: max %v", maxDev)
	}
}

func TestSpreadDeterministicBySeed(t *testing.T) {
	run := func() []BulletState {
		w := newWorldWith(t, 1)
		place(w, 1, 500, 500)
		w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: 1, Fire: true}})
		tickN(w, 30)
		return w.Snapshot().Bullets
	}
	a, b := run(), run()
	if len(a) != len(b) || len(a) < 2 {
		t.Fatalf("runs diverged in bullet count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("bullet %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestRestartCoolsDown(t *testing.T) {
	w := NewWorld(roundConfig(1, 0))
	w.AddPlayer(1)
	w.players[1].Heat = 1
	w.players[1].Fire = true // держит огонь до конца раунда — heat не остывает
	tickN(w, 20)
	tickN(w, 20) // intermission 1 с
	if r := w.Snapshot().Round; r.Phase != PhasePlay {
		t.Fatalf("setup: phase = %q", r.Phase)
	}
	if h := playerState(t, w, 1).Heat; h != 0 {
		t.Fatalf("heat after restart = %v, want 0", h)
	}
}
