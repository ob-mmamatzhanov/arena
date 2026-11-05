package game

import "testing"

// eventsByTick крутит мир n тиков и возвращает события каждого тика.
func eventsByTick(w *World, n int) [][]Event {
	out := make([][]Event, n)
	for i := range n {
		w.Update(dt, nil)
		out[i] = w.Snapshot().Events
	}
	return out
}

// occurrences — сколько раз и в скольких тиках встретилось событие типа typ.
func occurrences(ticks [][]Event, typ EventType) (count, inTicks int) {
	for _, evs := range ticks {
		n := 0
		for _, e := range evs {
			if e.Type == typ {
				n++
			}
		}
		count += n
		if n > 0 {
			inTicks++
		}
	}
	return count, inTicks
}

func TestShotAndHitEventsAppearExactlyOnce(t *testing.T) {
	w := newWorldWith(t, 1, 2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500)

	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Fire: true}})
	first := w.Snapshot().Events
	if again := w.Snapshot().Events; len(again) != len(first) {
		t.Fatal("Snapshot must be a pure read: events changed on second call")
	}
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2}}) // огонь отпущен
	second := w.Snapshot().Events
	ticks := append([][]Event{first, second}, eventsByTick(w, 20)...)

	if n, in := occurrences(ticks, EventShot); n != 1 || in != 1 {
		t.Fatalf("shot events: %d in %d ticks, want exactly 1", n, in)
	}
	if n, in := occurrences(ticks, EventHit); n != 1 || in != 1 {
		t.Fatalf("hit events: %d in %d ticks, want exactly 1", n, in)
	}
	if n, _ := occurrences(ticks, EventKill); n != 0 {
		t.Fatalf("kill events: %d, want 0 (target survives)", n)
	}

	shot := first[0]
	if shot.Type != EventShot || shot.Player != 1 || shot.Pos != (Vec2{X: 100, Y: 500}) {
		t.Fatalf("shot event = %+v, want player 1 at (100, 500)", shot)
	}
	for _, evs := range ticks {
		for _, e := range evs {
			if e.Type == EventHit && (e.Target != 2 || e.Shooter != 1) {
				t.Fatalf("hit event = %+v, want 1 -> 2", e)
			}
		}
	}
}

func TestKillEventFollowsHit(t *testing.T) {
	w := newWorldWith(t, 1, 2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500)
	w.players[2].HP = w.cfg.BulletDamage

	fireOnce(w, 1, 0)
	for _, evs := range eventsByTick(w, 20) {
		if len(evs) == 0 {
			continue
		}
		// Задержка респауна в testConfig выключена — respawn в том же тике.
		if len(evs) != 3 || evs[0].Type != EventHit || evs[1].Type != EventKill || evs[2].Type != EventRespawn {
			t.Fatalf("kill tick events = %+v, want [hit, kill, respawn]", evs)
		}
		if k := evs[1]; k.Target != 2 || k.Shooter != 1 {
			t.Fatalf("kill event = %+v, want 1 -> 2", k)
		}
		return
	}
	t.Fatal("no kill tick")
}

func TestBulletHitEventOnWallAndBorder(t *testing.T) {
	cfg := testConfig()
	cfg.Obstacles = []Obstacle{{X: 200, Y: 450, W: 20, H: 100}}
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500) // стреляет в стену справа
	place(w, 2, 950, 300) // стреляет в правую границу

	w.Update(dt, map[PlayerID]Input{
		1: {Seq: 1, Angle: 0, Fire: true},
		2: {Seq: 1, Angle: 0, Fire: true},
	})
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2}, 2: {Seq: 2}})
	var got []Event
	got = append(got, w.Snapshot().Events...)
	for _, evs := range eventsByTick(w, 10) {
		got = append(got, evs...)
	}

	var wall, border []Vec2
	for _, e := range got {
		if e.Type != EventBulletHit {
			continue
		}
		if e.Pos.X < 500 {
			wall = append(wall, e.Pos)
		} else {
			border = append(border, e.Pos)
		}
	}
	// Центр пули в момент касания стены: X грани минус радиус пули.
	if len(wall) != 1 || !approx(wall[0].X, 200-cfg.BulletRadius) || !approx(wall[0].Y, 500) {
		t.Fatalf("wall bulletHit = %v, want one at (%v, 500)", wall, 200-cfg.BulletRadius)
	}
	if len(border) != 1 || !approx(border[0].X, cfg.Width) || !approx(border[0].Y, 300) {
		t.Fatalf("border bulletHit = %v, want one at (%v, 300)", border, cfg.Width)
	}
}

func TestExpiredBulletEmitsNoEvent(t *testing.T) {
	cfg := testConfig()
	cfg.BulletSpeed = 100 // за 1.5 с пролетит 150 и истечёт внутри мира
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	fireOnce(w, 1, 0)
	if n, _ := occurrences(eventsByTick(w, 40), EventBulletHit); n != 0 {
		t.Fatalf("expired bullet produced %d bulletHit events", n)
	}
	if len(w.Snapshot().Bullets) != 0 {
		t.Fatal("setup: bullet did not expire")
	}
}

func TestPickupEvent(t *testing.T) {
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	dropPickup(w, PickupSpeed, 500, 500)
	w.Update(dt, nil)
	evs := w.Snapshot().Events
	if len(evs) != 1 || evs[0] != (Event{Type: EventPickup, Player: 1, Pickup: PickupSpeed}) {
		t.Fatalf("events = %+v, want one pickup event for player 1 (speed)", evs)
	}
	w.Update(dt, nil)
	if evs := w.Snapshot().Events; len(evs) != 0 {
		t.Fatalf("pickup event leaked into next tick: %+v", evs)
	}
}

func TestNoEventsInQuietTick(t *testing.T) {
	w := newWorldWith(t, 1, 2)
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
	if evs := w.Snapshot().Events; len(evs) != 0 {
		t.Fatalf("quiet tick has events: %+v", evs)
	}
}
