package game

import (
	"math"
	"testing"
)

const dt = 0.05

func place(w *World, id PlayerID, x, y float64) { w.players[id].Pos = Vec2{X: x, Y: y} }

// fireOnce: один тик с Fire=true, затем ввод с Fire=false (seq 2).
func fireOnce(w *World, id PlayerID, angle float64) {
	w.Update(dt, map[PlayerID]Input{id: {Seq: 1, Angle: angle, Fire: true}})
	w.Update(dt, map[PlayerID]Input{id: {Seq: 2, Angle: angle}})
}

// runCollectHits крутит мир n тиков без ввода и собирает все попадания.
func runCollectHits(w *World, n int) []Hit {
	var hits []Hit
	for range n {
		w.Update(dt, nil)
		hits = append(hits, w.Snapshot().Hits...)
	}
	return hits
}

func TestFireSpawnsBulletInAimDirection(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)

	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: math.Pi / 2, Fire: true}})
	s := w.Snapshot()
	if len(s.Bullets) != 1 {
		t.Fatalf("bullets = %d, want 1", len(s.Bullets))
	}
	b := s.Bullets[0]
	off := cfg.PlayerRadius + cfg.BulletRadius
	if b.Owner != 1 || !approx(b.Pos.X, 500) || !approx(b.Pos.Y, 500+off) {
		t.Fatalf("bullet = %+v, want owner 1 at (500, %v)", b, 500+off)
	}
	if !approx(b.Vel.X, 0) || !approx(b.Vel.Y, cfg.BulletSpeed) {
		t.Fatalf("bullet vel = %v, want (0, %v)", b.Vel, cfg.BulletSpeed)
	}

	// Следующий тик: пуля летит, новой нет (кулдаун).
	w.Update(dt, nil)
	s = w.Snapshot()
	if len(s.Bullets) != 1 || !approx(s.Bullets[0].Pos.Y, 500+off+cfg.BulletSpeed*dt) {
		t.Fatalf("after tick bullets = %+v", s.Bullets)
	}
}

func TestFireRespectsCooldown(t *testing.T) {
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	// Fire зажат: стреляем на тиках 1, 5, 9 (кулдаун 0.2 = 4 тика).
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: math.Pi, Fire: true}})
	for range 9 {
		w.Update(dt, nil)
	}
	if got := len(w.Snapshot().Bullets); got != 3 {
		t.Fatalf("bullets after 10 ticks = %d, want 3", got)
	}

	// Отпустили огонь — новых пуль нет.
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2, Angle: math.Pi}})
	ids := map[BulletID]bool{}
	for range 4 {
		w.Update(dt, nil)
		for _, b := range w.Snapshot().Bullets {
			ids[b.ID] = true
		}
	}
	if len(ids) != 3 {
		t.Fatalf("distinct bullets after release = %d, want 3", len(ids))
	}
}

func TestBulletHitsOtherPlayer(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1, 2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500)

	fireOnce(w, 1, 0)
	hits := runCollectHits(w, 20)

	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want exactly 1", hits)
	}
	h := hits[0]
	if h.Target != 2 || h.Shooter != 1 || h.Killed {
		t.Fatalf("hit = %+v, want target 2 shooter 1 not killed", h)
	}
	if d := math.Hypot(h.Pos.X-300, h.Pos.Y-500); d > cfg.PlayerRadius+cfg.BulletRadius+eps {
		t.Fatalf("hit pos %v too far from target (%v)", h.Pos, d)
	}
	if hp := playerState(t, w, 2).HP; hp != StartHP-cfg.BulletDamage {
		t.Fatalf("target hp = %d, want %d", hp, StartHP-cfg.BulletDamage)
	}
	if hp := playerState(t, w, 1).HP; hp != StartHP {
		t.Fatalf("shooter hp = %d, want %d (no self-hit)", hp, StartHP)
	}
	if n := len(w.Snapshot().Bullets); n != 0 {
		t.Fatalf("bullet not removed after hit: %d left", n)
	}
}

func TestHitsAreOnlyInTickSnapshot(t *testing.T) {
	w := newWorldWith(t, 1, 2)
	place(w, 1, 100, 500)
	place(w, 2, 140, 500)
	fireOnce(w, 1, 0) // цель вплотную — попадание на втором тике
	if len(w.Snapshot().Hits) != 1 {
		t.Fatalf("hits on hit tick = %+v, want 1", w.Snapshot().Hits)
	}
	w.Update(dt, nil)
	if len(w.Snapshot().Hits) != 0 {
		t.Fatalf("hits leaked to next tick: %+v", w.Snapshot().Hits)
	}
}

func TestKillGivesScoreAndRespawns(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1, 2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500)
	w.players[2].HP = cfg.BulletDamage // умрёт с одного попадания

	fireOnce(w, 1, 0)
	hits := runCollectHits(w, 20)

	if len(hits) != 1 || !hits[0].Killed {
		t.Fatalf("hits = %+v, want one kill", hits)
	}
	victim := playerState(t, w, 2)
	if victim.HP != StartHP || victim.Pos != w.spawnPoint(2) {
		t.Fatalf("victim = %+v, want respawn at %v with %d hp", victim, w.spawnPoint(2), StartHP)
	}
	if s := playerState(t, w, 1).Score; s != 1 {
		t.Fatalf("shooter score = %d, want 1", s)
	}
}

func TestFastBulletDoesNotTunnel(t *testing.T) {
	cfg := testConfig()
	cfg.BulletSpeed = 10000 // 500 единиц за тик — больше диаметра цели
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	place(w, 2, 400, 500)

	fireOnce(w, 1, 0)
	hits := append(w.Snapshot().Hits, runCollectHits(w, 5)...)
	if len(hits) != 1 || hits[0].Target != 2 {
		t.Fatalf("hits = %+v, want one hit on player 2", hits)
	}
}

func TestBulletHitsNearestTargetFirst(t *testing.T) {
	cfg := testConfig()
	cfg.BulletSpeed = 10000
	w := NewWorld(cfg)
	for _, id := range []PlayerID{1, 2, 3} {
		w.AddPlayer(id)
	}
	place(w, 1, 100, 500)
	place(w, 2, 600, 500) // дальний, но меньший ID
	place(w, 3, 300, 500)

	fireOnce(w, 1, 0)
	hits := append(w.Snapshot().Hits, runCollectHits(w, 5)...)
	if len(hits) != 1 || hits[0].Target != 3 {
		t.Fatalf("hits = %+v, want one hit on nearest player 3", hits)
	}
}

func TestBulletRemovedOutsideWorld(t *testing.T) {
	w := newWorldWith(t, 1)
	place(w, 1, 980, 500)
	fireOnce(w, 1, 0)
	runCollectHits(w, 2)
	if n := len(w.Snapshot().Bullets); n != 0 {
		t.Fatalf("bullets outside world not removed: %d", n)
	}
}

func TestBulletExpires(t *testing.T) {
	cfg := testConfig()
	cfg.BulletSpeed = 100 // за время жизни (1 с) пролетит 100 — остаётся в мире
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 500, 500)

	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Fire: true}})
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2}})
	runCollectHits(w, 17) // прожила 0.9 с
	if n := len(w.Snapshot().Bullets); n != 1 {
		t.Fatalf("bullet expired too early: %d", n)
	}
	runCollectHits(w, 2)
	if n := len(w.Snapshot().Bullets); n != 0 {
		t.Fatalf("bullet did not expire: %d", n)
	}
}

func TestSnapshotBulletsIsolated(t *testing.T) {
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Fire: true}})
	s := w.Snapshot()
	saved := s.Bullets[0]
	w.Update(dt, nil)
	if s.Bullets[0] != saved {
		t.Fatal("Update changed bullets of an already taken snapshot")
	}
}
