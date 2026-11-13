package game

import (
	"math"
	"slices"
	"testing"
)

// zombieConfig — testConfig (1000×1000 без препятствий) с режимом зомби.
func zombieConfig() Config {
	cfg := ZombieModeConfig(testConfig())
	cfg.IntermissionTime = 2
	return cfg
}

// ticksFor — число тиков dt в s секундах.
func ticksFor(s float64) int { return int(math.Round(s / dt)) }

// putZombie добавляет неподвижного зомби (Speed 0) и считает волну идущей.
func putZombie(w *World, x, y float64) *Zombie {
	w.zg.started, w.zg.inWave = true, true
	w.zg.nextID++
	w.zg.zombies = append(w.zg.zombies, Zombie{ID: w.zg.nextID, Pos: Vec2{X: x, Y: y}, HP: w.cfg.Zombie.HP})
	return &w.zg.zombies[len(w.zg.zombies)-1]
}

func TestFirstWaveFiveSecondsAfterFirstPlayer(t *testing.T) {
	w := NewWorld(zombieConfig())
	tickN(w, 200) // без игроков отсчёт не идёт
	if s := w.Snapshot(); len(s.Zombies) != 0 || s.Round.Wave != 0 {
		t.Fatalf("зомби без игроков: %d, волна %d", len(s.Zombies), s.Round.Wave)
	}

	w.AddPlayer(1)
	// Вход применяется в тике T (комната добавляет игрока перед Update), волна — в T+100.
	tickN(w, ticksFor(5))
	s := w.Snapshot()
	if len(s.Zombies) != 0 || s.Round.Wave != 0 || math.Abs(s.Round.TimeLeft-dt) > 1e-9 {
		t.Fatalf("за тик до 5 с: зомби %d, волна %d, timeLeft %v", len(s.Zombies), s.Round.Wave, s.Round.TimeLeft)
	}
	tickN(w, 1)
	s = w.Snapshot()
	if len(s.Zombies) != 2 || s.Round.Wave != 1 || s.Round.ZombiesLeft != 2 || s.Round.TimeLeft != 0 {
		t.Fatalf("через 5 с: зомби %d, round %+v", len(s.Zombies), s.Round)
	}
	for _, z := range s.Zombies {
		if z.HP != 30 || z.Speed != 90 {
			t.Fatalf("зомби первой волны %+v, want hp 30, speed 90", z)
		}
	}
}

func TestWavesGrowAndPauseTenSeconds(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	tickN(w, ticksFor(5)+1)
	for wave := 1; wave <= 3; wave++ {
		s := w.Snapshot()
		wantN, wantSpeed := 2+2*(wave-1), 90+5*float64(wave-1)
		if s.Round.Wave != wave || len(s.Zombies) != wantN || s.Zombies[0].Speed != wantSpeed {
			t.Fatalf("волна %d: round %+v, зомби %d (want %d), скорость %v (want %v)",
				wave, s.Round, len(s.Zombies), wantN, s.Zombies[0].Speed, wantSpeed)
		}
		for i := range w.zg.zombies { // вся волна убита
			w.zg.zombies[i].HP = 0
		}
		tickN(w, ticksFor(10))
		if s := w.Snapshot(); s.Round.Wave != wave || len(s.Zombies) != 0 || s.Round.TimeLeft <= 0 {
			t.Fatalf("пауза после волны %d: round %+v, зомби %d", wave, s.Round, len(s.Zombies))
		}
		tickN(w, 1)
	}
}

func TestZombieSpawnAtEdgeAwayFromPlayers(t *testing.T) {
	for seed := range uint64(20) {
		cfg := ZombieModeConfig(DefaultConfig()) // карта с препятствиями
		cfg.Seed = seed
		w := NewWorld(cfg)
		for id := PlayerID(1); id <= 4; id++ {
			w.AddPlayer(id)
		}
		r := cfg.Zombie.Radius
		for wave := 1; wave <= 3; wave++ {
			w.zg.zombies = w.zg.zombies[:0]
			w.zg.inWave, w.zg.nextWave = false, dt
			tickN(w, 1)
			ids := w.sortedIDs()
			for _, z := range w.zg.zombies {
				atEdge := math.Abs(z.Pos.X-r) < 1e-9 || math.Abs(z.Pos.X-(cfg.Width-r)) < 1e-9 ||
					math.Abs(z.Pos.Y-r) < 1e-9 || math.Abs(z.Pos.Y-(cfg.Height-r)) < 1e-9
				// Позиция — после первого шага (до 90·dt от точки спавна).
				step := z.Speed*dt + 1e-9
				if !atEdge && !nearEdge(z.Pos, r, step, cfg) {
					t.Fatalf("seed %d волна %d: зомби %+v не у края", seed, wave, z.Pos)
				}
				if !w.circleFree(z.Pos, r) {
					t.Fatalf("seed %d: зомби %+v в препятствии", seed, z.Pos)
				}
				if _, d2 := w.nearestAlive(z.Pos, ids); math.Sqrt(d2) < cfg.Zombie.SpawnMinPlayerDist-step {
					t.Fatalf("seed %d: зомби %+v в %.0f px от игрока", seed, z.Pos, math.Sqrt(d2))
				}
			}
		}
	}
}

func nearEdge(p Vec2, r, step float64, cfg Config) bool {
	return p.X-r <= step || cfg.Width-r-p.X <= step || p.Y-r <= step || cfg.Height-r-p.Y <= step
}

func TestPlayersDoNotHurtEachOther(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	place(w, 2, 200, 500)
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: 0, Fire: true}})
	var evs []Event
	for range 10 {
		w.Update(dt, nil)
		evs = append(evs, w.Snapshot().Events...)
	}
	if hp := playerState(t, w, 2).HP; hp != StartHP || len(ofType(evs, EventHit)) != 0 {
		t.Fatalf("пуля ранила игрока: hp %d, события %+v", hp, evs)
	}
}

func TestBulletsKillZombie(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	place(w, 1, 100, 500)
	z := putZombie(w, 200, 500)
	zid := z.ID

	// Выстрел и 6 тиков полёта (100 px при 25 px за тик) — события всех тиков.
	shot := func(seq uint32) []Event {
		w.Update(dt, map[PlayerID]Input{1: {Seq: seq, Angle: 0, Fire: true}})
		evs := slices.Clone(w.Snapshot().Events)
		w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 1, Angle: 0}})
		evs = append(evs, w.Snapshot().Events...)
		for range 6 {
			w.Update(dt, nil)
			evs = append(evs, w.Snapshot().Events...)
		}
		return evs
	}
	if evs := shot(1); len(ofType(evs, EventBulletHit)) != 1 || len(ofType(evs, EventZombieKill)) != 0 {
		t.Fatalf("первая пуля (30 hp − 25): события %+v", evs)
	}
	evs := shot(3)
	kills := ofType(evs, EventZombieKill)
	if len(kills) != 1 || kills[0].Zombie != zid || kills[0].Shooter != 1 || kills[0].Pos.X != 200 {
		t.Fatalf("вторая пуля: события %+v, want zombieKill зомби %d от игрока 1", evs, zid)
	}
	s := w.Snapshot()
	if len(s.Zombies) != 0 || s.Round.Kills != 1 || playerState(t, w, 1).Score != 1 {
		t.Fatalf("после убийства: зомби %d, kills %d, score %d", len(s.Zombies), s.Round.Kills, playerState(t, w, 1).Score)
	}
}

func TestZombieContactDamageWithCooldown(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	putZombie(w, 500+w.cfg.PlayerRadius+w.cfg.Zombie.Radius, 500) // касание
	w.zg.zombies[0].Speed = 0

	hp := func() int { return playerState(t, w, 1).HP }
	w.Update(dt, nil)
	if hp() != 90 {
		t.Fatalf("после касания hp %d, want 90", hp())
	}
	if e, ok := findEvent(w.Snapshot().Events, EventHit); !ok || e.Target != 1 || e.Shooter != 0 {
		t.Fatalf("нет hit с shooter 0: %+v", w.Snapshot().Events)
	}
	tickN(w, ticksFor(0.5)-1)
	if hp() != 90 {
		t.Fatalf("укус раньше кулдауна 0.5 с: hp %d", hp())
	}
	tickN(w, 1)
	if hp() != 80 {
		t.Fatalf("через 0.5 с hp %d, want 80", hp())
	}

	// Неуязвимого не кусают.
	w.players[1].invulnLeft = 1
	tickN(w, ticksFor(0.9))
	if hp() != 80 {
		t.Fatalf("укус неуязвимого: hp %d", hp())
	}
}

func TestDeadPlayerRevivesAtNextWave(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 500, 500)
	place(w, 2, 100, 100)
	putZombie(w, 500+w.cfg.PlayerRadius+w.cfg.Zombie.Radius, 500)
	w.zg.zombies[0].Speed = 0
	w.zg.wave = 1
	w.players[1].HP = 10

	w.Update(dt, nil)
	if !playerState(t, w, 1).Dead {
		t.Fatal("игрок с 10 hp пережил укус")
	}
	if e, ok := findEvent(w.Snapshot().Events, EventKill); !ok || e.Target != 1 || e.Shooter != 0 {
		t.Fatalf("нет kill с shooter 0: %+v", w.Snapshot().Events)
	}
	tickN(w, ticksFor(30)) // таймера респауна нет
	if !playerState(t, w, 1).Dead {
		t.Fatal("игрок воскрес до конца волны")
	}

	w.zg.zombies[0].HP = 0 // волна зачищена
	w.Update(dt, nil)
	if ps := playerState(t, w, 1); !ps.Dead || math.Abs(ps.RespawnIn-10) > 1e-9 {
		t.Fatalf("в паузе: %+v, want мёртв, respawnIn 10", ps)
	}
	tickN(w, ticksFor(10))
	if ps := playerState(t, w, 1); ps.Dead || ps.HP != StartHP || w.Snapshot().Round.Wave != 2 {
		t.Fatalf("в начале волны 2: %+v, волна %d", ps, w.Snapshot().Round.Wave)
	}
}

func TestAllDeadIsGameOverThenRestart(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	putZombie(w, 500+w.cfg.PlayerRadius+w.cfg.Zombie.Radius, 500)
	w.zg.zombies[0].Speed = 0
	w.zg.wave, w.zg.kills = 3, 7
	w.players[1].HP = 10

	w.Update(dt, nil)
	s := w.Snapshot()
	if s.Round.Phase != PhaseGameOver || s.Round.Wave != 3 || s.Round.Kills != 7 || w.Phase() != PhaseGameOver {
		t.Fatalf("все мертвы: round %+v", s.Round)
	}
	// Мир заморожен: ввод не применяется.
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1}})
	if playerState(t, w, 1).LastSeq != 0 {
		t.Fatal("ввод применён в gameover")
	}
	tickN(w, ticksFor(2)-1)
	s = w.Snapshot()
	if s.Round.Phase != PhasePlay || s.Round.Wave != 0 || s.Round.Kills != 0 || len(s.Zombies) != 0 {
		t.Fatalf("после gameover: round %+v, зомби %d", s.Round, len(s.Zombies))
	}
	if ps := playerState(t, w, 1); ps.Dead || ps.HP != StartHP || math.Abs(s.Round.TimeLeft-5) > 1e-9 {
		t.Fatalf("новая игра: %+v, timeLeft %v", ps, s.Round.TimeLeft)
	}
}

func TestZombieGameResetsWhenEveryoneLeaves(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	tickN(w, ticksFor(5)+1)
	if len(w.Snapshot().Zombies) != 2 {
		t.Fatal("волна 1 не началась")
	}
	w.RemovePlayer(1)
	tickN(w, 1)
	if s := w.Snapshot(); len(s.Zombies) != 0 || s.Round.Wave != 0 {
		t.Fatalf("без игроков: зомби %d, волна %d", len(s.Zombies), s.Round.Wave)
	}
	w.AddPlayer(2)
	tickN(w, ticksFor(5)+1)
	if s := w.Snapshot(); len(s.Zombies) != 2 || s.Round.Wave != 1 {
		t.Fatalf("после нового входа: зомби %d, волна %d", len(s.Zombies), s.Round.Wave)
	}
}

// Стена от верха до y = 800 между зомби и игроком: напрямую не пройти,
// по flow field — в обход через проход внизу, не цепляясь за угол стены.
// Во втором случае в стене щель 16 px против зомби: пуля пролетит, тело 24 —
// нет, и путь через неё вёл бы в тупик.
func TestZombieWalksAroundWallByFlowField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		walls []Obstacle
	}{
		{"сплошная стена", []Obstacle{{X: 480, Y: 0, W: 40, H: 800}}},
		// Щель y 102…118: в неё попадает центр клетки (y 110) — проверка одного
		// центра сочла бы клетку проходимой.
		{"щель 16 px", []Obstacle{{X: 480, Y: 0, W: 40, H: 102}, {X: 480, Y: 118, W: 40, H: 682}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := zombieConfig()
			cfg.Obstacles = tc.walls
			w := NewWorld(cfg)
			w.AddPlayer(1)
			place(w, 1, 700, 110)
			putZombie(w, 300, 110)
			w.zg.zombies[0].Speed = 200
			w.rebuildFlow(w.sortedIDs())

			reach := cfg.PlayerRadius + cfg.Zombie.Radius + 1
			for i := range ticksFor(20) {
				w.Update(dt, nil)
				z := w.zg.zombies[0].Pos
				if !w.circleFree(z, cfg.Zombie.Radius) {
					t.Fatalf("тик %d: зомби %+v в стене", i, z)
				}
				if p := w.players[1].Pos; math.Hypot(p.X-z.X, p.Y-z.Y) <= reach {
					return
				}
			}
			t.Fatalf("зомби не дошёл до игрока за 20 с: %+v", w.zg.zombies[0].Pos)
		})
	}
}

func TestFlowFieldRebuiltEveryFiveTicks(t *testing.T) {
	w := NewWorld(zombieConfig())
	w.AddPlayer(1)
	place(w, 1, 100, 100)
	putZombie(w, 900, 900)
	w.zg.zombies[0].Speed = 0
	tickN(w, 1) // пересчёт: flowWait был 0
	f := &w.zg.flow
	if f.dist[f.cellOf(Vec2{X: 100, Y: 100})] != 0 {
		t.Fatal("клетка игрока не засеяна")
	}
	place(w, 1, 500, 500)
	newCell := f.cellOf(Vec2{X: 500, Y: 500})
	tickN(w, 4)
	if f.dist[newCell] == 0 {
		t.Fatal("поле пересчитано раньше 5 тиков")
	}
	tickN(w, 1)
	if f.dist[newCell] != 0 {
		t.Fatal("поле не пересчитано на 5-м тике")
	}
}

func TestZombieModeDeterministic(t *testing.T) {
	run := func() []ZombieState {
		cfg := ZombieModeConfig(DefaultConfig())
		cfg.Seed = 42
		w := NewWorld(cfg)
		w.AddPlayer(1)
		w.AddPlayer(2)
		tickN(w, ticksFor(8))
		return w.Snapshot().Zombies
	}
	if a, b := run(), run(); len(a) == 0 || !slices.Equal(a, b) {
		t.Fatalf("разные прогоны с одним seed:\n%+v\n%+v", a, b)
	}
}

func TestZombieUpdateDoesNotAllocate(t *testing.T) {
	cfg := ZombieModeConfig(DefaultConfig())
	cfg.Seed = 1
	w := NewWorld(cfg)
	for id := PlayerID(1); id <= 4; id++ {
		w.AddPlayer(id)
	}
	tickN(w, ticksFor(5)+1) // волна идёт
	inputs := map[PlayerID]Input{}
	seq := uint32(0)
	allocs := testing.AllocsPerRun(100, func() {
		seq++
		for id := PlayerID(1); id <= 4; id++ {
			inputs[id] = Input{Seq: seq, MoveX: 1, Angle: 0, Fire: true}
		}
		w.Update(dt, inputs)
	})
	if allocs != 0 {
		t.Fatalf("Update в режиме зомби: %v аллокаций за тик", allocs)
	}
}

func TestZombieModeConfigDisablesRoundLimits(t *testing.T) {
	cfg := ZombieModeConfig(DefaultConfig())
	if cfg.RoundTime != 0 || cfg.KillLimit != 0 || cfg.Zombie != DefaultZombieConfig() {
		t.Fatalf("ZombieModeConfig: RoundTime %v KillLimit %d Zombie %+v", cfg.RoundTime, cfg.KillLimit, cfg.Zombie)
	}
	if NewWorld(DefaultConfig()).zombiesOn() {
		t.Fatal("режим зомби включён в DefaultConfig")
	}
	if s := NewWorld(DefaultConfig()).Snapshot(); s.Round.Zombies || s.Zombies != nil {
		t.Fatalf("снапшот арены с полями зомби: %+v", s.Round)
	}
}

// На картах продакшена неподвижный игрок в случайной свободной точке (или на
// спавне) за 30 с дожидается всех зомби волны: никто не застревает у
// препятствий. Ловит и путь сквозь щели между ящиками, и цепляние за углы.
func TestZombiesReachPlayerOnProductionMaps(t *testing.T) {
	for _, maxPlayers := range []int{2, LargeMapMinPlayers} {
		for seed := range uint64(20) {
			cfg := ZombieModeConfig(ConfigForPlayers(maxPlayers))
			cfg.Seed = seed
			w := NewWorld(cfg)
			w.AddPlayer(1)
			pos := w.respawnPoint(1, 0)
			for seed%2 == 1 { // половина прогонов — случайная свободная точка
				p := Vec2{X: 20 + w.rng.Float64()*(cfg.Width-40), Y: 20 + w.rng.Float64()*(cfg.Height-40)}
				if w.isFree(p) {
					pos = p
					break
				}
			}
			tickN(w, ticksFor(5)+1) // волна 1
			place(w, 1, pos.X, pos.Y)
			w.players[1].HP = math.MaxInt32 // укусы не убивают
			tickN(w, ticksFor(30))

			reach := cfg.PlayerRadius + cfg.Zombie.Radius + 2
			for _, z := range w.zg.zombies {
				if d := math.Hypot(z.Pos.X-pos.X, z.Pos.Y-pos.Y); d > reach {
					t.Errorf("карта %s, seed %d, игрок %.0f,%.0f: зомби застрял в %.0f,%.0f (%.0f px)",
						cfg.Map.Name, seed, pos.X, pos.Y, z.Pos.X, z.Pos.Y, d)
				}
			}
		}
	}
}
