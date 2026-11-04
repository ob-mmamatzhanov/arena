package game

import (
	"math"
	"testing"
)

// dashDist — полный путь рывка: интеграл линейно падающей скорости.
func dashDist(cfg Config) float64 { return cfg.DashSpeed * cfg.DashDuration / 2 }

func TestDashImpulseDecaysLinearly(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 300, 500)

	// Тик 1 — рывок вправо (по движению). Путь по тикам — площади трапеций
	// под v(τ) = 700·τ/0.15: 29.1(6), 17.5, 5.8(3).
	want := []float64{29.0 + 1.0/6, 17.5, 5.0 + 5.0/6}
	x := 300.0
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1, Dash: true}})
	for i, d := range want {
		if i > 0 {
			w.Update(dt, nil)
		}
		got := playerState(t, w, 1).Pos.X
		if !approx(got-x, d) {
			t.Fatalf("dash tick %d: moved %v, want %v", i+1, got-x, d)
		}
		x = got
	}
	if total := x - 300; !approx(total, dashDist(cfg)) {
		t.Fatalf("dash total = %v, want %v", total, dashDist(cfg))
	}
	// После рывка — снова обычное движение (MoveX=1 из последнего ввода).
	w.Update(dt, nil)
	if d := playerState(t, w, 1).Pos.X - x; !approx(d, cfg.PlayerSpeed*dt) {
		t.Fatalf("after dash moved %v, want normal %v", d, cfg.PlayerSpeed*dt)
	}
}

func TestDashUsesAngleWhenIdle(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: math.Pi / 2, Dash: true}})
	tickN(w, 3)
	p := playerState(t, w, 1).Pos
	if !approx(p.X, 500) || !approx(p.Y, 500+dashDist(cfg)) {
		t.Fatalf("idle dash ended at %v, want (500, %v) along angle", p, 500+dashDist(cfg))
	}
}

func TestDashDoesNotFireOnCooldown(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 100, 500)

	// Кулдаун отсчитывается с тиков после рывка: в тике рывка он только выставляется.
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: 0, Dash: true}})
	tickN(w, 20) // 1 с после рывка
	if cd := playerState(t, w, 1).DashCd; !approx(cd, cfg.DashCooldown-1) {
		t.Fatalf("dashCd after 1 s = %v, want %v", cd, cfg.DashCooldown-1)
	}

	// Повторный dash на кулдауне: игрок стоит, импульса нет.
	before := playerState(t, w, 1).Pos
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2, Angle: 0, Dash: true}})
	tickN(w, 3)
	if after := playerState(t, w, 1).Pos; after != before {
		t.Fatalf("dash on cooldown moved player %v -> %v", before, after)
	}

	// Через 3 с после первого рывка — снова доступен.
	tickN(w, 60-24)
	if cd := playerState(t, w, 1).DashCd; cd != 0 {
		t.Fatalf("dashCd after 3 s = %v, want 0", cd)
	}
	x := playerState(t, w, 1).Pos.X
	w.Update(dt, map[PlayerID]Input{1: {Seq: 3, Angle: 0, Dash: true}})
	tickN(w, 2)
	if d := playerState(t, w, 1).Pos.X - x; !approx(d, dashDist(cfg)) {
		t.Fatalf("dash after cooldown moved %v, want %v", d, dashDist(cfg))
	}
}

func TestDashRequestIsOneShot(t *testing.T) {
	cfg := testConfig()
	cfg.DashCooldown = 0.1 // кулдаун короче паузы — повтор был бы возможен
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 100, 500)
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Dash: true}})
	tickN(w, 20) // новых вводов нет — запрос не должен повторяться
	if x := playerState(t, w, 1).Pos.X; !approx(x, 100+dashDist(cfg)) {
		t.Fatalf("x = %v, want exactly one dash to %v", x, 100+dashDist(cfg))
	}
}

func TestInputIgnoredDuringDash(t *testing.T) {
	run := func(during Input) Vec2 {
		w := newWorldWith(t, 1)
		place(w, 1, 300, 500)
		w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1, Dash: true}})
		during.Seq = 2
		w.Update(dt, map[PlayerID]Input{1: during})
		w.Update(dt, nil)
		return playerState(t, w, 1).Pos
	}
	ref := run(Input{MoveX: 1})
	for _, in := range []Input{
		{MoveX: -1},
		{MoveY: 1},
		{MoveX: -1, MoveY: -1, Dash: true},
	} {
		if got := run(in); got != ref {
			t.Fatalf("input %+v during dash changed trajectory: %v, want %v", in, got, ref)
		}
	}
}

func TestDashStopsAtObstacleAndBounds(t *testing.T) {
	cfg := testConfig()
	cfg.Obstacles = []Obstacle{{X: 330, Y: 400, W: 40, H: 200}}
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 300, 500) // стена в 30 − r = 20 справа
	place(w, 2, 30, 500)  // граница в 30 − r = 20 слева

	w.Update(dt, map[PlayerID]Input{
		1: {Seq: 1, MoveX: 1, Dash: true},
		2: {Seq: 1, MoveX: -1, Dash: true},
	})
	tickN(w, 3)
	if x := playerState(t, w, 1).Pos.X; !approx(x, 330-cfg.PlayerRadius) {
		t.Fatalf("dash into wall: x = %v, want stopped at %v", x, 330-cfg.PlayerRadius)
	}
	if x := playerState(t, w, 2).Pos.X; !approx(x, cfg.PlayerRadius) {
		t.Fatalf("dash into border: x = %v, want stopped at %v", x, cfg.PlayerRadius)
	}
}
