package game

import (
	"math"
	"testing"
)

func worldWithObstacles(t *testing.T, obs ...Obstacle) *World {
	t.Helper()
	cfg := testConfig()
	cfg.Obstacles = obs
	return NewWorld(cfg)
}

func TestPlayerCannotEnterObstacle(t *testing.T) {
	wall := Obstacle{X: 500, Y: 300, W: 40, H: 400}
	w := worldWithObstacles(t, wall)
	w.AddPlayer(1)
	place(w, 1, 400, 500)
	r := w.cfg.PlayerRadius

	for i := uint32(1); i <= 40; i++ { // 40 тиков × 5 = 200 единиц, стена в 88
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, MoveX: 1}})
	}
	if got := playerState(t, w, 1).Pos.X; !approx(got, wall.X-r) {
		t.Fatalf("x = %v, want stopped at contact %v", got, wall.X-r)
	}
}

func TestSlideAlongWallDoesNotPassThrough(t *testing.T) {
	wall := Obstacle{X: 500, Y: 300, W: 40, H: 400}
	w := worldWithObstacles(t, wall)
	w.AddPlayer(1)
	r := w.cfg.PlayerRadius
	place(w, 1, wall.X-r, 320) // прижат к левой грани
	startY := 320.0

	// Жмём по диагонали в стену: X упирается, Y должен скользить.
	for i := uint32(1); i <= 60; i++ {
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, MoveX: 1, MoveY: 1}})
		p := playerState(t, w, 1).Pos
		if !w.isFree(p) {
			t.Fatalf("tick %d: player inside wall at %v", i, p)
		}
		if p.Y > wall.Y && p.Y < wall.Y+wall.H && p.X > wall.X-r+eps {
			t.Fatalf("tick %d: player passed into wall band at %v", i, p)
		}
	}
	got := playerState(t, w, 1).Pos
	// Диагональ нормализована: по Y — speed/√2 × 3 с ≈ 212.
	wantY := startY + w.cfg.PlayerSpeed/math.Sqrt2*60*dt
	if !approx(got.X, wall.X-r) || !approx(got.Y, wantY) {
		t.Fatalf("pos = %v, want sliding to (%v, %v)", got, wall.X-r, wantY)
	}
}

func TestPlayerSlidesAroundCornerWithoutEnteringIt(t *testing.T) {
	box := Obstacle{X: 500, Y: 500, W: 100, H: 100}
	w := worldWithObstacles(t, box)
	w.AddPlayer(1)
	place(w, 1, 450, 420) // выше-левее угла (500, 500)
	for i := uint32(1); i <= 80; i++ {
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, MoveX: 1, MoveY: 1}})
		if p := playerState(t, w, 1).Pos; !w.isFree(p) {
			t.Fatalf("tick %d: player inside box at %v", i, p)
		}
	}
	// Обогнул угол: правее коробки и ниже её верхней грани.
	if p := playerState(t, w, 1).Pos; p.X <= box.X+box.W || p.Y <= box.Y {
		t.Fatalf("player stuck at corner: %v", p)
	}
}

func TestThinWallIsNotTunneled(t *testing.T) {
	cfg := testConfig()
	cfg.PlayerSpeed = 2000 // 100 единиц за тик — больше толщины стены и диаметра
	cfg.Obstacles = []Obstacle{{X: 500, Y: 0, W: 1, H: 1000}}
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 450, 500)
	for i := uint32(1); i <= 5; i++ {
		w.Update(dt, map[PlayerID]Input{1: {Seq: i, MoveX: 1}})
	}
	if got := playerState(t, w, 1).Pos.X; !approx(got, 500-cfg.PlayerRadius) {
		t.Fatalf("x = %v, want stopped before thin wall at %v", got, 500-cfg.PlayerRadius)
	}
}

func TestBulletRemovedOnObstacle(t *testing.T) {
	w := worldWithObstacles(t, Obstacle{X: 200, Y: 450, W: 20, H: 100})
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500) // за стеной

	fireOnce(w, 1, 0)
	if n := len(w.Snapshot().Bullets); n != 1 {
		t.Fatalf("bullet should still fly before the wall: %d", n)
	}
	hits := runCollectHits(w, 20)
	if len(hits) != 0 {
		t.Fatalf("bullet went through the wall: %+v", hits)
	}
	if hp := playerState(t, w, 2).HP; hp != StartHP {
		t.Fatalf("target behind wall hp = %d, want %d", hp, StartHP)
	}
	if n := len(w.Snapshot().Bullets); n != 0 {
		t.Fatalf("bullet not removed on obstacle: %d left", n)
	}
}

func TestBulletHitsPlayerInFrontOfObstacle(t *testing.T) {
	w := worldWithObstacles(t, Obstacle{X: 330, Y: 450, W: 20, H: 100})
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500) // перед стеной: за один тик пуля задевает обоих

	fireOnce(w, 1, 0)
	hits := runCollectHits(w, 20)
	if len(hits) != 1 || hits[0].Target != 2 {
		t.Fatalf("hits = %+v, want one hit on player 2 in front of the wall", hits)
	}
}

func TestRespawnOnlyInFreePoint(t *testing.T) {
	// Препятствие закрывает 90% карты: свободна только полоса y > 900.
	for seed := uint64(1); seed <= 20; seed++ {
		cfg := testConfig()
		cfg.Seed = seed
		cfg.Obstacles = []Obstacle{{X: 0, Y: 0, W: 1000, H: 900}}
		w := NewWorld(cfg)
		w.AddPlayer(1)
		w.AddPlayer(2)
		place(w, 1, 100, 950)
		place(w, 2, 300, 950)
		w.players[2].HP = cfg.BulletDamage

		fireOnce(w, 1, 0)
		hits := runCollectHits(w, 20)
		if len(hits) != 1 || !hits[0].Killed {
			t.Fatalf("seed %d: hits = %+v, want one kill", seed, hits)
		}
		if p := w.players[2].Pos; !w.isFree(p) {
			t.Fatalf("seed %d: respawned inside obstacle at %v", seed, p)
		}
	}
}
