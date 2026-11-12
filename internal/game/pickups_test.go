package game

import (
	"math"
	"slices"
	"testing"
)

func dropPickup(w *World, typ PickupType, x, y float64) {
	w.nextPickup++
	w.pickups = append(w.pickups, Pickup{ID: w.nextPickup, Type: typ, Pos: Vec2{X: x, Y: y}})
}

func TestPickupSpawnsOnIntervalUpToMax(t *testing.T) {
	cfg := testConfig()
	cfg.PickupInterval = 1
	w := NewWorld(cfg)
	w.AddPlayer(1)

	tickN(w, 19)
	if n := len(w.Snapshot().Pickups); n != 0 {
		t.Fatalf("pickups before first interval = %d, want 0", n)
	}
	tickN(w, 1)
	if n := len(w.Snapshot().Pickups); n != 1 {
		t.Fatalf("pickups after 1 s = %d, want 1", n)
	}
	tickN(w, 20*10)
	ps := w.Snapshot().Pickups
	if len(ps) != cfg.MaxPickups {
		t.Fatalf("pickups after 11 s = %d, want capped at %d", len(ps), cfg.MaxPickups)
	}
	for _, p := range ps {
		if p.Type != PickupHealth && p.Type != PickupSpeed {
			t.Fatalf("общий поток дал пикап %q, want health или speed", p.Type)
		}
	}
}

// Ammo идёт своим потоком: свой интервал и лимит, общий лимит его не блокирует.
func TestAmmoPickupStreamIndependent(t *testing.T) {
	cfg := testConfig()
	cfg.PickupInterval, cfg.MaxPickups = 1, 3
	cfg.AmmoPickupInterval, cfg.MaxAmmoPickups = 0.5, 2
	w := NewWorld(cfg)
	w.AddPlayer(1)

	ammo := func() int { return w.countPickups(true) }
	tickN(w, 9) // 0.45 с
	if n := ammo(); n != 0 {
		t.Fatalf("ammo до первого интервала = %d, want 0", n)
	}
	tickN(w, 1) // 0.5 с
	if n := ammo(); n != 1 {
		t.Fatalf("ammo через 0.5 с = %d, want 1", n)
	}
	tickN(w, 10) // 1 с
	if n := ammo(); n != 2 {
		t.Fatalf("ammo через 1 с = %d, want 2", n)
	}
	tickN(w, 20*10) // общий поток упёрся в свой лимит, ammo — в свой
	if n, g := ammo(), w.countPickups(false); n != cfg.MaxAmmoPickups || g != cfg.MaxPickups {
		t.Fatalf("через 11 с ammo = %d, общих = %d, want %d и %d", n, g, cfg.MaxAmmoPickups, cfg.MaxPickups)
	}

	// Подобрали один ammo — следующий появится не позже чем через интервал.
	pk := w.pickups[slices.IndexFunc(w.pickups, func(p Pickup) bool { return p.Type == PickupAmmo })]
	place(w, 1, pk.Pos.X, pk.Pos.Y)
	w.Update(dt, nil)
	if n := ammo(); n != 1 {
		t.Fatalf("после подбора ammo = %d, want 1", n)
	}
	tickN(w, 10)
	if n := ammo(); n != 2 {
		t.Fatalf("через интервал после подбора ammo = %d, want 2", n)
	}
}

func TestAmmoPickupStreamDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.AmmoPickupInterval, cfg.MaxAmmoPickups = 0, 2
	w := NewWorld(cfg)
	w.AddPlayer(1)
	tickN(w, 20*30)
	if n := len(w.Snapshot().Pickups); n != 0 {
		t.Fatalf("пикапы при выключенных потоках = %d, want 0", n)
	}
}

func TestPickupNeverSpawnsInObstacleOrNearPlayer(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		cfg := testConfig()
		cfg.Seed = seed
		cfg.PickupInterval = dt // каждый тик
		cfg.MaxPickups = 1000
		cfg.Obstacles = []Obstacle{
			{X: 0, Y: 0, W: 1000, H: 400},
			{X: 0, Y: 600, W: 1000, H: 400},
		}
		w := NewWorld(cfg)
		// Свободна только полоса y ∈ [400, 600]; игроки стоят в ней цепочкой.
		for i := range 8 {
			id := PlayerID(i + 1)
			w.AddPlayer(id)
			place(w, id, 60+float64(i)*120, 500)
		}

		seen := 0
		for range 100 {
			w.Update(dt, nil)
			for _, pk := range w.Snapshot().Pickups[seen:] {
				if !w.circleFree(pk.Pos, cfg.PickupRadius) {
					t.Fatalf("seed %d: pickup %d inside obstacle at %v", seed, pk.ID, pk.Pos)
				}
				for _, p := range w.Snapshot().Players {
					if d := math.Hypot(pk.Pos.X-p.Pos.X, pk.Pos.Y-p.Pos.Y); d < cfg.PickupMinPlayerDist {
						t.Fatalf("seed %d: pickup %d at %v only %.1f from player %d", seed, pk.ID, pk.Pos, d, p.ID)
					}
				}
			}
			seen = len(w.Snapshot().Pickups)
		}
		if seen == 0 {
			t.Fatalf("seed %d: no pickups spawned in the free strip", seed)
		}
	}
}

// 20 последовательных спавнов (10 из общего потока и 10 ammo): центры любых
// двух пикапов не ближе PickupMinPickupDist, до края мира — не ближе
// PickupEdgeMargin. Оба потока проверяют дистанцию и к пикапам другого потока.
func TestPickupSpawnsKeepDistanceFromEachOther(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		cfg := testConfig()
		cfg.Seed = seed
		cfg.Width, cfg.Height = 400, 400 // тесно: ограничение действительно срабатывает
		cfg.PickupInterval, cfg.MaxPickups = dt, 10
		cfg.AmmoPickupInterval, cfg.MaxAmmoPickups = dt, 10
		cfg.PickupMinPickupDist, cfg.PickupEdgeMargin = 60, 50
		w := NewWorld(cfg)

		for i := 0; len(w.pickups) < 20; i++ {
			if i == 200 {
				t.Fatalf("seed %d: за %d тиков появилось лишь %d пикапов из 20", seed, i, len(w.pickups))
			}
			w.Update(dt, nil)
		}
		pks := w.Snapshot().Pickups
		for i, a := range pks {
			if a.Pos.X < 50 || a.Pos.X > cfg.Width-50 || a.Pos.Y < 50 || a.Pos.Y > cfg.Height-50 {
				t.Errorf("seed %d: пикап %d в %v ближе 50 к краю мира", seed, a.ID, a.Pos)
			}
			for _, b := range pks[i+1:] {
				if d := math.Hypot(a.Pos.X-b.Pos.X, a.Pos.Y-b.Pos.Y); d < 60 {
					t.Errorf("seed %d: пикапы %d (%s) и %d (%s) в %.1f друг от друга, нужно ≥ 60",
						seed, a.ID, a.Type, b.ID, b.Type, d)
				}
			}
		}
	}
}

// Отступ от края действует и на точки из зон карты: зона у самого края даёт
// пикапы только в своей части, отстоящей от края на PickupEdgeMargin.
func TestPickupZoneRespectsEdgeMargin(t *testing.T) {
	cfg := testConfig()
	cfg.PickupInterval, cfg.MaxPickups = dt, 1000
	cfg.PickupEdgeMargin = 50
	cfg.PickupZones = map[PickupType][]Zone{
		PickupHealth: {RectZone(0, 0, 100, 100)},
		PickupSpeed:  {RectZone(900, 900, 100, 100)},
	}
	w := NewWorld(cfg)
	tickN(w, 200)
	if len(w.pickups) == 0 {
		t.Fatal("пикапы не появились")
	}
	for _, pk := range w.pickups {
		p := pk.Pos
		if p.X < 50 || p.X > cfg.Width-50 || p.Y < 50 || p.Y > cfg.Height-50 {
			t.Errorf("пикап %s в %v ближе 50 к краю мира", pk.Type, p)
		}
	}
}

// Новый пикап не встаёт ближе PickupMinPickupDist к уже лежащему: единственное
// свободное место занято — спавн ждёт, пока пикап не подберут.
func TestPickupWaitsForDistanceFromOtherPickup(t *testing.T) {
	cfg := testConfig()
	cfg.PickupInterval, cfg.MaxPickups = 1, 10
	cfg.PickupMinPickupDist = 60
	cfg.PickupZones = map[PickupType][]Zone{
		PickupHealth: {CircleZone(500, 500, 20)},
		PickupSpeed:  {CircleZone(500, 500, 20)},
	}
	w := NewWorld(cfg)
	dropPickup(w, PickupHealth, 500, 500)
	tickN(w, 100) // 5 с, интервал 1 с
	if len(w.pickups) != 1 {
		t.Fatalf("пикапов %d, нужен 1: зона целиком ближе 60 к лежащему", len(w.pickups))
	}
	w.pickups = w.pickups[:0]
	tickN(w, 1)
	if len(w.pickups) != 1 {
		t.Fatalf("после освобождения места пикапов %d, нужен 1", len(w.pickups))
	}
}

func TestPickupWaitsForFreeSpot(t *testing.T) {
	cfg := testConfig()
	cfg.PickupInterval = 1
	cfg.Obstacles = []Obstacle{{X: 0, Y: 0, W: 1000, H: 1000}} // места нет совсем
	w := NewWorld(cfg)
	tickN(w, 40)
	if n := len(w.Snapshot().Pickups); n != 0 {
		t.Fatalf("pickup spawned with no free space: %d", n)
	}
	w.cfg.Obstacles = nil // место освободилось — спавн на следующем же тике
	tickN(w, 1)
	if n := len(w.Snapshot().Pickups); n != 1 {
		t.Fatalf("pickup not spawned once space freed: %d", n)
	}
}

func TestHealthPickupCapsAt100(t *testing.T) {
	tests := []struct{ hp, want int }{{30, 80}, {80, 100}, {100, 100}}
	for _, tt := range tests {
		w := newWorldWith(t, 1)
		place(w, 1, 500, 500)
		w.players[1].HP = tt.hp
		dropPickup(w, PickupHealth, 505, 500)
		w.Update(dt, nil)
		if got := playerState(t, w, 1).HP; got != tt.want {
			t.Errorf("hp %d + health = %d, want %d", tt.hp, got, tt.want)
		}
		if n := len(w.Snapshot().Pickups); n != 0 {
			t.Errorf("hp %d: pickup not consumed", tt.hp)
		}
	}
}

func TestPickupRequiresOverlap(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 500, 500)
	r := cfg.PlayerRadius + cfg.PickupRadius
	dropPickup(w, PickupHealth, 500+r+0.5, 500) // чуть дальше касания
	w.Update(dt, nil)
	if n := len(w.Snapshot().Pickups); n != 1 {
		t.Fatalf("pickup collected without overlap")
	}
}

func TestSpeedPickupExpiresAfter5s(t *testing.T) {
	cfg := testConfig()
	w := newWorldWith(t, 1)
	place(w, 1, 100, 500)
	dropPickup(w, PickupSpeed, 100, 500)
	w.Update(dt, nil) // подобран, без движения
	if !playerState(t, w, 1).Boosted {
		t.Fatal("not boosted after speed pickup")
	}

	step := func(seq uint32) float64 {
		x := playerState(t, w, 1).Pos.X
		w.Update(dt, map[PlayerID]Input{1: {Seq: seq, MoveX: 1}})
		return playerState(t, w, 1).Pos.X - x
	}
	boosted := cfg.PlayerSpeed * cfg.SpeedMultiplier * dt
	normal := cfg.PlayerSpeed * dt
	for i := uint32(1); i <= 100; i++ { // 5 с ускорения
		if d := step(i); !approx(d, boosted) {
			t.Fatalf("tick %d of boost: moved %v, want %v", i, d, boosted)
		}
	}
	if playerState(t, w, 1).Boosted {
		t.Fatal("still boosted after 5 s")
	}
	if d := step(101); !approx(d, normal) {
		t.Fatalf("after boost: moved %v, want %v", d, normal)
	}
}

func TestPickupsClearedOnNewRound(t *testing.T) {
	cfg := testConfig()
	cfg.RoundTime, cfg.IntermissionTime = 1, 1
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 100, 100)
	dropPickup(w, PickupHealth, 800, 800)
	w.players[1].speedLeft = 3
	tickN(w, 20)
	if s := w.Snapshot(); s.Round.Phase != PhaseIntermission || len(s.Pickups) != 1 {
		t.Fatalf("setup: phase %q, pickups %d", s.Round.Phase, len(s.Pickups))
	}
	tickN(w, 20)
	s := w.Snapshot()
	if s.Round.Phase != PhasePlay || len(s.Pickups) != 0 {
		t.Fatalf("after restart: phase %q, pickups %+v, want play and none", s.Round.Phase, s.Pickups)
	}
	if playerState(t, w, 1).Boosted {
		t.Fatal("speed boost survived round restart")
	}
}

// Поток ammo масштабируется числом игроков: до AmmoBasePlayers — как в конфиге,
// дальше интервал ×base/n, лимит ×n/base вверх (патронов на игрока как в дуэли).
func TestAmmoStreamScalesWithPlayers(t *testing.T) {
	cfg := DefaultConfig() // 5 с, 2 коробки на 2 игроков
	w := NewWorld(cfg)
	tests := []struct {
		n        int
		interval float64
		limit    int
	}{
		{0, 5, 2}, {1, 5, 2}, {2, 5, 2},
		{3, 5 * 2.0 / 3, 3}, // 2·3/2 = 3
		{5, 2, 5},           // 2·5/2 = 5
		{8, 1.25, 8},
	}
	for _, tc := range tests {
		iv, lim := w.ammoStream(tc.n)
		if math.Abs(iv-tc.interval) > 1e-9 || lim != tc.limit {
			t.Errorf("n=%d: интервал %.3f, лимит %d; want %.3f, %d", tc.n, iv, lim, tc.interval, tc.limit)
		}
	}

	cfg.AmmoBasePlayers = 0 // масштабирование выключено
	w = NewWorld(cfg)
	if iv, lim := w.ammoStream(8); iv != cfg.AmmoPickupInterval || lim != cfg.MaxAmmoPickups {
		t.Errorf("без AmmoBasePlayers при 8 игроках: %.2f, %d; want как в конфиге", iv, lim)
	}
}

// Бой 8 человек на большой арене: патроны появляются в 4 раза чаще и до 8
// коробок на карте — все находят место в зонах ammo этой карты.
func TestAmmoForEightPlayersOnLargeMap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Map = ArenaLargeMap()
	cfg.Seed = 7
	w := NewWorld(cfg)
	for id := PlayerID(1); id <= 8; id++ {
		w.AddPlayer(id)
	}
	// 1.25 с на коробку: через 5 с — 4 (при 2 игроках была бы ещё 1).
	tickN(w, 5*20)
	if n := w.countPickups(true); n != 4 {
		t.Fatalf("ammo через 5 с при 8 игроках = %d, want 4", n)
	}
	// Через 12 с — упор в лимит 8; все в зонах ammo.
	tickN(w, 7*20)
	if n := w.countPickups(true); n != 8 {
		t.Fatalf("ammo через 12 с при 8 игроках = %d, want лимит 8", n)
	}
	for _, pk := range w.pickups {
		if pk.Type != PickupAmmo {
			continue
		}
		in := false
		for _, z := range cfg.PickupZones[PickupAmmo] {
			in = in || z.contains(pk.Pos)
		}
		if !in {
			t.Errorf("ammo %v вне зон патронов", pk.Pos)
		}
	}
}

// Карта по вместимости комнаты: до LargeMapMinPlayers — разреженная арена,
// с неё — арена с дополнительными укрытиями.
func TestMapForPlayers(t *testing.T) {
	for n, want := range map[int]string{2: "arena", 4: "arena", 5: "arena-large", 8: "arena-large"} {
		if got := MapForPlayers(n).Name; got != want {
			t.Errorf("MapForPlayers(%d) = %s, want %s", n, got, want)
		}
	}
	small, large := ArenaMap(), ArenaLargeMap()
	if len(large.Obstacles) <= len(small.Obstacles) {
		t.Errorf("на большой арене укрытий %d, на обычной %d", len(large.Obstacles), len(small.Obstacles))
	}
	// ArenaLargeMap не меняет общую ArenaMap (срезы и карта зон скопированы).
	if len(ArenaMap().Obstacles) != len(small.Obstacles) || len(ArenaMap().PickupZones[PickupAmmo]) != 2 {
		t.Error("ArenaLargeMap изменила ArenaMap")
	}
}
