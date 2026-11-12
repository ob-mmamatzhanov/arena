package game

import (
	"slices"
	"testing"
)

// Поле 1000×1000: ящик 32×32 (индекс 0) на линии y = 106 и кирпич 32×32
// (индекс 1) на линии y = 216. Стрелок 1 стоит левее, цель 2 — за препятствием.
func crateWorld(t *testing.T, crateHits int) *World {
	t.Helper()
	cfg := testConfig()
	cfg.Obstacles = []Obstacle{
		{X: 300, Y: 90, W: 32, H: 32, Kind: ObstacleCrate},
		{X: 300, Y: 200, W: 32, H: 32},
	}
	cfg.CrateHits = crateHits
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	return w
}

// shoot: игрок 1 стреляет вправо и ждёт 16 тиков (0.8 с): пуля пролетает до
// 375 ед., а heat успевает остыть — каждый выстрел точный. Возвращает события
// всех тиков.
func shoot(w *World) []Event {
	var evs []Event
	w.Update(dt, map[PlayerID]Input{1: {Seq: w.players[1].LastSeq + 1, Angle: 0, Fire: true}})
	evs = append(evs, w.events...)
	w.Update(dt, map[PlayerID]Input{1: {Seq: w.players[1].LastSeq + 1, Angle: 0}})
	evs = append(evs, w.events...)
	for range 14 {
		w.Update(dt, nil)
		evs = append(evs, w.events...)
	}
	return evs
}

func ofType(evs []Event, typ EventType) []Event {
	return slices.DeleteFunc(slices.Clone(evs), func(e Event) bool { return e.Type != typ })
}

func TestCrateBreaksAfterFiveHits(t *testing.T) {
	w := crateWorld(t, 5)
	place(w, 1, 100, 106)
	place(w, 2, 400, 106) // за ящиком

	for i := 1; i <= 4; i++ {
		evs := shoot(w)
		hits := ofType(evs, EventCrateHit)
		if len(hits) != 1 || hits[0].Obstacle != 0 || hits[0].HP != 5-i {
			t.Fatalf("выстрел %d: crateHit = %+v, want один с Obstacle 0, HP %d", i, hits, 5-i)
		}
		if len(ofType(evs, EventCrateBroken)) != 0 || len(ofType(evs, EventHit)) != 0 {
			t.Fatalf("выстрел %d: ящик сломан раньше времени или пуля прошла сквозь него: %+v", i, evs)
		}
		if got := w.Snapshot().Crates; !slices.Equal(got, []CrateState{{Index: 0, HP: 5 - i}}) {
			t.Fatalf("выстрел %d: снапшот crates = %+v, want [{0 %d}]", i, got, 5-i)
		}
	}

	evs := shoot(w) // пятое попадание
	hits, brk := ofType(evs, EventCrateHit), ofType(evs, EventCrateBroken)
	if len(hits) != 1 || hits[0].HP != 0 {
		t.Fatalf("5-е попадание: crateHit = %+v, want HP 0", hits)
	}
	if len(brk) != 1 || brk[0].Obstacle != 0 || brk[0].Pos != (Vec2{X: 316, Y: 106}) {
		t.Fatalf("crateBroken = %+v, want Obstacle 0 в центре ящика (316,106)", brk)
	}
	if len(ofType(evs, EventHit)) != 0 {
		t.Fatal("ломающая пуля пролетела дальше — должна погаснуть о ящик")
	}
	if got := w.Snapshot().Crates; !slices.Equal(got, []CrateState{{Index: 0, HP: 0}}) {
		t.Fatalf("после поломки crates = %+v, want [{0 0}]", got)
	}

	// Сломанного ящика нет: следующая пуля попадает в того, кто за ним прятался.
	evs = shoot(w)
	if h := ofType(evs, EventHit); len(h) != 1 || h[0].Target != 2 {
		t.Fatalf("после поломки пуля должна попасть в игрока 2, события %+v", evs)
	}
	if len(ofType(evs, EventCrateHit)) != 0 {
		t.Fatal("попадание в уже сломанный ящик")
	}
}

func TestBrokenCrateIsPassable(t *testing.T) {
	w := crateWorld(t, 1)
	place(w, 1, 100, 106)
	place(w, 2, 900, 900)
	if w.isFree(Vec2{X: 316, Y: 106}) {
		t.Fatal("целый ящик: точка внутри него свободна")
	}
	shoot(w) // CrateHits = 1 — ломается с первого попадания
	if !w.isFree(Vec2{X: 316, Y: 106}) {
		t.Fatal("сломанный ящик всё ещё занимает место")
	}
	place(w, 1, 260, 106)
	for range 20 { // 1 с вправо: с целым ящиком упёрся бы в x = 300 − 10
		w.Update(dt, map[PlayerID]Input{1: {Seq: w.players[1].LastSeq + 1, MoveX: 1}})
	}
	if x := w.players[1].Pos.X; x < 340 {
		t.Fatalf("игрок не прошёл сквозь сломанный ящик: x = %.1f", x)
	}
}

func TestBrickAndDisabledCratesAreIndestructible(t *testing.T) {
	t.Run("кирпич", func(t *testing.T) {
		w := crateWorld(t, 5)
		place(w, 1, 100, 216) // линия кирпича
		place(w, 2, 900, 900)
		for range 7 {
			evs := shoot(w)
			if len(ofType(evs, EventBulletHit)) != 1 || len(ofType(evs, EventCrateHit)) != 0 {
				t.Fatalf("кирпич: события %+v, want только bulletHit", evs)
			}
		}
		if c := w.Snapshot().Crates; c != nil {
			t.Fatalf("кирпич попал в crates: %+v", c)
		}
	})
	t.Run("CrateHits=0", func(t *testing.T) {
		w := crateWorld(t, 0)
		place(w, 1, 100, 106)
		place(w, 2, 900, 900)
		for range 7 {
			evs := shoot(w)
			if len(ofType(evs, EventBulletHit)) != 1 || len(ofType(evs, EventCrateHit)) != 0 {
				t.Fatalf("CrateHits=0: события %+v, want только bulletHit", evs)
			}
		}
		if w.isFree(Vec2{X: 316, Y: 106}) {
			t.Fatal("CrateHits=0: ящик пропал")
		}
	})
}

func TestCratesRestoredAtRoundStart(t *testing.T) {
	w := crateWorld(t, 2)
	place(w, 1, 100, 106)
	place(w, 2, 900, 900)
	shoot(w)
	shoot(w)
	if !w.isFree(Vec2{X: 316, Y: 106}) {
		t.Fatal("ящик не сломан после 2 попаданий при CrateHits = 2")
	}
	w.startRound()
	if w.isFree(Vec2{X: 316, Y: 106}) || w.Snapshot().Crates != nil {
		t.Fatalf("новый раунд не восстановил ящик: crates = %+v", w.Snapshot().Crates)
	}
	// Ящик снова держит полные CrateHits попаданий.
	place(w, 1, 100, 106)
	tickN(w, 30) // неуязвимость и кулдаун после старта раунда
	if h := ofType(shoot(w), EventCrateHit); len(h) != 1 || h[0].HP != 1 {
		t.Fatalf("после восстановления crateHit = %+v, want HP 1", h)
	}
}

// Ящики на настоящих картах ломаются, кирпич — нет; у каждой карты есть ящики.
func TestMapCratesDestructibleByDefault(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		cfg := DefaultConfig()
		cfg.Map = m
		w := NewWorld(cfg)
		crates := 0
		for i, o := range m.Obstacles {
			want := o.KindOrDefault() == ObstacleCrate
			if want {
				crates++
			}
			if w.destructible(i) != want {
				t.Errorf("препятствие %d %+v: разрушаемое = %v, want %v", i, o, w.destructible(i), want)
			}
		}
		if crates == 0 {
			t.Error("на карте нет ящиков")
		}
	})
}

// Большая арена — прочные ящики: комнаты на 5–8 игроков получают
// LargeMapCrateHits, остальные — DefaultConfig().CrateHits.
func TestConfigForPlayersCrateHits(t *testing.T) {
	for n, want := range map[int]int{2: DefaultConfig().CrateHits, 4: DefaultConfig().CrateHits, 5: LargeMapCrateHits, 8: LargeMapCrateHits} {
		cfg := ConfigForPlayers(n)
		if cfg.CrateHits != want || cfg.Map.Name != MapForPlayers(n).Name {
			t.Errorf("ConfigForPlayers(%d): CrateHits %d, карта %s; want %d, %s", n, cfg.CrateHits, cfg.Map.Name, want, MapForPlayers(n).Name)
		}
		// Мир с этим конфигом выдаёт ящикам ровно столько прочности.
		w := NewWorld(cfg)
		for i, o := range cfg.Obstacles {
			if o.KindOrDefault() == ObstacleCrate && w.crateHP[i] != want {
				t.Fatalf("ConfigForPlayers(%d): ящик %d с прочностью %d, want %d", n, i, w.crateHP[i], want)
			}
		}
	}
	if LargeMapCrateHits != 10 {
		t.Errorf("LargeMapCrateHits = %d, want 10", LargeMapCrateHits)
	}
}
