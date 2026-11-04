package game

import (
	"math"
	"slices"
	"testing"
)

const eps = 1e-9

func testConfig() Config {
	return Config{
		Width: 1000, Height: 1000, PlayerSpeed: 100, PlayerRadius: 10, HitRadius: 12,
		BulletSpeed: 500, BulletRadius: 3, BulletLifetime: 1.5, FireCooldown: 0.2, BulletDamage: 25,
		HeatPerShot: 0.2, HeatDecay: 0.5, MaxSpread: 0.25,
		Seed: 1,
	}
}

func newWorldWith(t *testing.T, ids ...PlayerID) *World {
	t.Helper()
	w := NewWorld(testConfig())
	for _, id := range ids {
		w.AddPlayer(id)
	}
	return w
}

func playerState(t *testing.T, w *World, id PlayerID) PlayerState {
	t.Helper()
	for _, p := range w.Snapshot().Players {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("player %d not in snapshot", id)
	return PlayerState{}
}

func approx(a, b float64) bool { return math.Abs(a-b) < eps }

func TestAddPlayer(t *testing.T) {
	w := newWorldWith(t, 1)
	if !w.HasPlayer(1) || w.PlayerCount() != 1 {
		t.Fatalf("player not added: count=%d", w.PlayerCount())
	}

	before := playerState(t, w, 1)
	w.Update(1, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
	w.AddPlayer(1) // повторное добавление не должно сбрасывать состояние
	after := playerState(t, w, 1)
	if w.PlayerCount() != 1 || approx(after.Pos.X, before.Pos.X) {
		t.Fatalf("re-adding player reset state: before=%v after=%v", before.Pos, after.Pos)
	}
}

func TestSpawnPointsDistinctAndInside(t *testing.T) {
	cfg := testConfig()
	w := NewWorld(cfg)
	for id := PlayerID(1); id <= 8; id++ {
		w.AddPlayer(id)
	}
	seen := map[Vec2]PlayerID{}
	for _, p := range w.Snapshot().Players {
		if p.Pos.X < cfg.PlayerRadius || p.Pos.X > cfg.Width-cfg.PlayerRadius ||
			p.Pos.Y < cfg.PlayerRadius || p.Pos.Y > cfg.Height-cfg.PlayerRadius {
			t.Errorf("player %d spawned outside arena: %v", p.ID, p.Pos)
		}
		if other, ok := seen[p.Pos]; ok {
			t.Errorf("players %d and %d share spawn point %v", other, p.ID, p.Pos)
		}
		seen[p.Pos] = p.ID
	}
}

func TestRemovePlayer(t *testing.T) {
	w := newWorldWith(t, 1, 2)
	w.RemovePlayer(1)
	w.RemovePlayer(1) // идемпотентно
	w.RemovePlayer(42)
	if w.HasPlayer(1) || !w.HasPlayer(2) || w.PlayerCount() != 1 {
		t.Fatalf("unexpected players after remove: %+v", w.Snapshot().Players)
	}
}

func TestUpdateMovesPlayer(t *testing.T) {
	w := newWorldWith(t, 1)
	start := playerState(t, w, 1).Pos

	w.Update(0.5, map[PlayerID]Input{1: {Seq: 1, MoveX: 1, MoveY: -1}})

	got := playerState(t, w, 1)
	// Диагональ нормализуется: смещение по каждой оси = speed*dt/√2.
	d := 100 * 0.5 / math.Sqrt2
	if !approx(got.Pos.X, start.X+d) || !approx(got.Pos.Y, start.Y-d) {
		t.Fatalf("pos = %v, want (%v, %v)", got.Pos, start.X+d, start.Y-d)
	}
	if got.LastSeq != 1 {
		t.Fatalf("LastSeq = %d, want 1", got.LastSeq)
	}
	if w.Tick() != 1 {
		t.Fatalf("Tick = %d, want 1", w.Tick())
	}
}

func TestUpdateKeepsLastDirectionWithoutInput(t *testing.T) {
	w := newWorldWith(t, 1)
	start := playerState(t, w, 1).Pos

	w.Update(0.1, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
	w.Update(0.1, nil)

	if got := playerState(t, w, 1).Pos.X; !approx(got, start.X+20) {
		t.Fatalf("x = %v, want %v", got, start.X+20)
	}
}

func TestUpdateStopsOnZeroInput(t *testing.T) {
	w := newWorldWith(t, 1)
	w.Update(0.1, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
	mid := playerState(t, w, 1).Pos
	w.Update(0.1, map[PlayerID]Input{1: {Seq: 2}})
	if got := playerState(t, w, 1).Pos; got != mid {
		t.Fatalf("player moved after zero input: %v -> %v", mid, got)
	}
}

func TestUpdateIgnoresStaleSeq(t *testing.T) {
	w := newWorldWith(t, 1)
	w.Update(0.1, map[PlayerID]Input{1: {Seq: 5, MoveX: 1}})
	w.Update(0.1, map[PlayerID]Input{1: {Seq: 5, MoveX: -1}}) // тот же seq
	w.Update(0.1, map[PlayerID]Input{1: {Seq: 3, MoveX: -1}}) // старый seq

	start := NewWorld(testConfig()).spawnPoint(1)
	got := playerState(t, w, 1)
	if got.LastSeq != 5 {
		t.Fatalf("LastSeq = %d, want 5", got.LastSeq)
	}
	if !approx(got.Pos.X, start.X+30) {
		t.Fatalf("x = %v, want %v (stale inputs must be ignored)", got.Pos.X, start.X+30)
	}
}

func TestUpdateIgnoresUnknownPlayer(t *testing.T) {
	w := newWorldWith(t, 1)
	w.Update(0.1, map[PlayerID]Input{99: {Seq: 1, MoveX: 1}})
	if w.HasPlayer(99) || w.PlayerCount() != 1 {
		t.Fatal("input for unknown player must not create it")
	}
}

func TestUpdateClampsToArena(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	w.Update(100, map[PlayerID]Input{1: {Seq: 1, MoveX: -1, MoveY: 1}})
	got := playerState(t, w, 1).Pos
	if !approx(got.X, cfg.PlayerRadius) || !approx(got.Y, cfg.Height-cfg.PlayerRadius) {
		t.Fatalf("pos = %v, want clamped to (%v, %v)", got, cfg.PlayerRadius, cfg.Height-cfg.PlayerRadius)
	}
}

func TestUpdateSanitizesInput(t *testing.T) {
	tests := []struct {
		name           string
		in             Input
		wantDX, wantDY float64 // смещение за dt=1 при speed=100
	}{
		{"oversized vector is normalized", Input{Seq: 1, MoveX: 1000}, 100, 0},
		{"short vector is kept", Input{Seq: 1, MoveX: 0.5}, 50, 0},
		{"NaN is zeroed", Input{Seq: 1, MoveX: math.NaN(), MoveY: 1}, 0, 0},
		{"Inf is zeroed", Input{Seq: 1, MoveX: math.Inf(1)}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorldWith(t, 1)
			start := playerState(t, w, 1).Pos
			w.Update(1, map[PlayerID]Input{1: tt.in})
			got := playerState(t, w, 1).Pos
			if !approx(got.X-start.X, tt.wantDX) || !approx(got.Y-start.Y, tt.wantDY) {
				t.Fatalf("delta = (%v, %v), want (%v, %v)", got.X-start.X, got.Y-start.Y, tt.wantDX, tt.wantDY)
			}
		})
	}
}

func TestUpdateRejectsBadDT(t *testing.T) {
	for _, dt := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		w := newWorldWith(t, 1)
		start := playerState(t, w, 1)
		w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
		if got := playerState(t, w, 1); got != start || w.Tick() != 0 {
			t.Errorf("dt=%v: state changed %+v -> %+v, tick=%d", dt, start, got, w.Tick())
		}
	}
}

func TestSnapshotSortedAndIsolated(t *testing.T) {
	w := newWorldWith(t, 5, 2, 8, 1)
	s := w.Snapshot()
	for i := 1; i < len(s.Players); i++ {
		if s.Players[i-1].ID >= s.Players[i].ID {
			t.Fatalf("snapshot not sorted: %+v", s.Players)
		}
	}

	// Снапшот — копия: его изменение не влияет на мир, а последующий Update — на снапшот.
	s.Players[0].Pos.X = -1
	saved := s.Players[1]
	w.Update(1, map[PlayerID]Input{2: {Seq: 1, MoveX: 1}})
	if playerState(t, w, 1).Pos.X == -1 {
		t.Fatal("mutating snapshot changed world")
	}
	if s.Players[1] != saved {
		t.Fatal("Update changed an already taken snapshot")
	}
}

func TestUpdateDeterministic(t *testing.T) {
	run := func() Snapshot {
		w := newWorldWith(t, 1, 2, 3)
		for i := uint32(1); i <= 50; i++ {
			w.Update(0.05, map[PlayerID]Input{
				1: {Seq: i, MoveX: 1, MoveY: 0.3, Angle: float64(i) * 0.1, Fire: true},
				2: {Seq: i, MoveX: -0.7, MoveY: float64(i%3) - 1, Angle: -float64(i) * 0.2, Fire: i%3 == 0},
				3: {Seq: i, MoveY: 1, Angle: 2, Fire: true},
			})
		}
		return w.Snapshot()
	}
	a, b := run(), run()
	if len(a.Players) != len(b.Players) || a.Tick != b.Tick {
		t.Fatal("runs diverged")
	}
	if !slices.Equal(a.Bullets, b.Bullets) || !slices.Equal(a.Hits, b.Hits) {
		t.Fatalf("bullets/hits diverged: %+v vs %+v", a.Bullets, b.Bullets)
	}
	for i := range a.Players {
		if a.Players[i] != b.Players[i] {
			t.Fatalf("runs diverged at %d: %+v vs %+v", i, a.Players[i], b.Players[i])
		}
	}
}

func TestNewPlayerHasStartHPAndZeroScore(t *testing.T) {
	w := newWorldWith(t, 1)
	got := playerState(t, w, 1)
	if got.HP != 100 || got.Score != 0 {
		t.Fatalf("new player hp=%d score=%d, want hp=100 score=0", got.HP, got.Score)
	}
}

func TestPlayerStaysInsideWorld(t *testing.T) {
	dirs := []struct{ x, y float64 }{
		{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1},
	}
	// Включая неквадратный мир 800×600: спавн и движение не должны выходить за его края.
	for _, cfg := range []Config{
		testConfig(),
		{Width: 800, Height: 600, PlayerSpeed: 200, PlayerRadius: 16},
	} {
		for _, d := range dirs {
			w := NewWorld(cfg)
			for id := PlayerID(1); id <= 8; id++ {
				w.AddPlayer(id)
			}
			inputs := map[PlayerID]Input{}
			for i := uint32(1); i <= 200; i++ {
				for id := PlayerID(1); id <= 8; id++ {
					inputs[id] = Input{Seq: i, MoveX: d.x, MoveY: d.y}
				}
				w.Update(0.05, inputs)
				for _, p := range w.Snapshot().Players {
					if p.Pos.X < 0 || p.Pos.X > cfg.Width || p.Pos.Y < 0 || p.Pos.Y > cfg.Height {
						t.Fatalf("world %vx%v dir %v: player %d out of bounds at %v",
							cfg.Width, cfg.Height, d, p.ID, p.Pos)
					}
				}
			}
		}
	}
}

func TestAngleFromInputInSnapshot(t *testing.T) {
	w := newWorldWith(t, 1)
	w.Update(0.05, map[PlayerID]Input{1: {Seq: 1, Angle: 1.25}})
	if got := playerState(t, w, 1).Angle; !approx(got, 1.25) {
		t.Fatalf("angle = %v, want 1.25", got)
	}

	// Без нового ввода и при устаревшем seq угол сохраняется.
	w.Update(0.05, nil)
	w.Update(0.05, map[PlayerID]Input{1: {Seq: 1, Angle: -2}})
	if got := playerState(t, w, 1).Angle; !approx(got, 1.25) {
		t.Fatalf("angle = %v after stale/no input, want 1.25", got)
	}

	// Свежий ввод перезаписывает угол; NaN игнорируется.
	w.Update(0.05, map[PlayerID]Input{1: {Seq: 2, Angle: -2}})
	w.Update(0.05, map[PlayerID]Input{1: {Seq: 3, Angle: math.NaN()}})
	if got := playerState(t, w, 1).Angle; !approx(got, -2) {
		t.Fatalf("angle = %v, want -2", got)
	}
}
