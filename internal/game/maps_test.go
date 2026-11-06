package game

import (
	"math"
	"testing"
)

// allMaps — все карты игры. Новая карта добавляется сюда и проходит те же проверки.
func allMaps() []Map {
	return []Map{ArenaMap()}
}

// Параметры принципов карты (см. ArenaMap).
const (
	mapLineStep       = 1.0   // шаг проверки сквозных линий: координаты карт целые
	mapMaxCoverDist   = 150.0 // укрытие не дальше
	mapMinPassage     = 32.0  // проход между препятствиями: диаметр игрока 24 + запас
	mapSpawnClearance = 60.0  // спавн не ближе к препятствию
	mapPlayerRadius   = 12.0  // DefaultConfig().PlayerRadius
	mapBulletRadius   = 3.0   // DefaultConfig().BulletRadius
	mapEps            = 1e-9
)

// slit — щель между двумя ящиками, выровненными по одной оси.
// Horizontal: щель — горизонтальная полоса Lo < y < Hi над/под ящиками
// с общим отрезком [From, To] по x; иначе — вертикальная полоса по x.
type slit struct {
	A, B       Obstacle
	Horizontal bool
	Lo, Hi     float64 // границы щели поперёк неё
	From, To   float64 // протяжённость щели вдоль неё
}

func (s slit) width() float64 { return s.Hi - s.Lo }
func (s slit) mid() float64   { return (s.Lo + s.Hi) / 2 }

// crateSlit — щель между a и b, если оба — ящики, стоят один над другим
// (или рядом) с совпадающими гранями, не касаются и зазор уже прохода mapMinPassage.
func crateSlit(a, b Obstacle) (slit, bool) {
	if a.KindOrDefault() != ObstacleCrate || b.KindOrDefault() != ObstacleCrate {
		return slit{}, false
	}
	if near(a.X, b.X) && near(a.W, b.W) {
		if a.Y > b.Y {
			a, b = b, a
		}
		if lo, hi := a.Y+a.H, b.Y; hi > lo+mapEps && hi-lo < mapMinPassage-mapEps {
			return slit{A: a, B: b, Horizontal: true, Lo: lo, Hi: hi, From: a.X, To: a.X + a.W}, true
		}
	}
	if near(a.Y, b.Y) && near(a.H, b.H) {
		if a.X > b.X {
			a, b = b, a
		}
		if lo, hi := a.X+a.W, b.X; hi > lo+mapEps && hi-lo < mapMinPassage-mapEps {
			return slit{A: a, B: b, Horizontal: false, Lo: lo, Hi: hi, From: a.Y, To: a.Y + a.H}, true
		}
	}
	return slit{}, false
}

// crateSlits — все щели между ящиками карты.
func crateSlits(m Map) []slit {
	var out []slit
	for i, a := range m.Obstacles {
		for _, b := range m.Obstacles[i+1:] {
			if s, ok := crateSlit(a, b); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func forEachMap(t *testing.T, f func(t *testing.T, m Map)) {
	for _, m := range allMaps() {
		t.Run(m.Name, func(t *testing.T) { f(t, m) })
	}
}

// rectDist — расстояние между прямоугольниками (0, если пересекаются или касаются).
func rectDist(a, b Obstacle) float64 {
	dx := math.Max(0, math.Max(a.X-(b.X+b.W), b.X-(a.X+a.W)))
	dy := math.Max(0, math.Max(a.Y-(b.Y+b.H), b.Y-(a.Y+a.H)))
	return math.Hypot(dx, dy)
}

// pointRectDist — расстояние от точки до прямоугольника (0 внутри).
func pointRectDist(p Vec2, o Obstacle) float64 {
	return math.Hypot(p.X-clamp(p.X, o.X, o.X+o.W), p.Y-clamp(p.Y, o.Y, o.Y+o.H))
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestMapRotationalSymmetry(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		for _, o := range m.Obstacles {
			want := Obstacle{X: m.Width - o.X - o.W, Y: m.Height - o.Y - o.H, W: o.W, H: o.H, Kind: o.KindOrDefault()}
			found := false
			for _, q := range m.Obstacles {
				if near(q.X, want.X) && near(q.Y, want.Y) && near(q.W, want.W) && near(q.H, want.H) &&
					q.KindOrDefault() == want.Kind {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("препятствию %+v нет пары %+v при повороте на 180°", o, want)
			}
		}
		for _, s := range m.Spawns {
			want := Vec2{X: m.Width - s.X, Y: m.Height - s.Y}
			found := false
			for _, q := range m.Spawns {
				if near(q.X, want.X) && near(q.Y, want.Y) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("спавну %v нет пары %v при повороте на 180°", s, want)
			}
		}
	})
}

func TestMapObstaclesInsideAndDisjoint(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		for i, a := range m.Obstacles {
			if a.W <= 0 || a.H <= 0 || a.X < 0 || a.Y < 0 || a.X+a.W > m.Width || a.Y+a.H > m.Height {
				t.Errorf("препятствие %+v вне карты %vx%v или вырождено", a, m.Width, m.Height)
			}
			for _, b := range m.Obstacles[i+1:] {
				// Пересечение внутренностей; касание тоже запрещено проверкой проходов ниже.
				if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
					t.Errorf("препятствия пересекаются: %+v и %+v", a, b)
				}
			}
		}
	})
}

func TestMapSpawnsClearOfObstacles(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		if len(m.Spawns) != 8 {
			t.Fatalf("спавнов %d, нужно 8", len(m.Spawns))
		}
		for _, s := range m.Spawns {
			if s.X < mapPlayerRadius || s.X > m.Width-mapPlayerRadius || s.Y < mapPlayerRadius || s.Y > m.Height-mapPlayerRadius {
				t.Errorf("спавн %v: игрок не помещается в карту", s)
			}
			for _, o := range m.Obstacles {
				if d := pointRectDist(s, o); d < mapSpawnClearance-mapEps {
					t.Errorf("спавн %v в %.1f px от препятствия %+v, нужно ≥ %v", s, d, o, mapSpawnClearance)
				}
			}
		}
		// По две точки на каждую сторону карты (ближайший край).
		sides := map[string]int{}
		for _, s := range m.Spawns {
			d := map[string]float64{"top": s.Y, "bottom": m.Height - s.Y, "left": s.X, "right": m.Width - s.X}
			best := "top"
			for k, v := range d {
				if v < d[best] {
					best = k
				}
			}
			sides[best]++
		}
		for _, side := range []string{"top", "bottom", "left", "right"} {
			if sides[side] != 2 {
				t.Errorf("у стороны %s %d спавнов, нужно 2 (%v)", side, sides[side], sides)
			}
		}
	})
}

// Нет сквозной линии: каждая горизонталь y = 1, 2, … и вертикаль x = 1, 2, …
// от края до края пересекает хотя бы одно препятствие. Линии по самому краю
// (y = 0, x = 0 …) не проверяются: это граница мира. При целых координатах
// препятствий шаг 1 px не пропускает ни одного просвета между проекциями.
// Линии через щели между ящиками (они проходят мимо самих ящиков) проверяются
// явно — на случай карты с дробными координатами.
func TestMapNoClearLine(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		var ys, xs []float64
		for y := mapLineStep; y < m.Height; y += mapLineStep {
			ys = append(ys, y)
		}
		for x := mapLineStep; x < m.Width; x += mapLineStep {
			xs = append(xs, x)
		}
		for _, s := range crateSlits(m) {
			// Края полосы, по которым ещё проходит пуля, и её середина.
			lines := []float64{s.Lo + mapBulletRadius, s.mid(), s.Hi - mapBulletRadius}
			if s.Horizontal {
				ys = append(ys, lines...)
			} else {
				xs = append(xs, lines...)
			}
		}
		for _, y := range ys {
			if !lineBlocked(m.Obstacles, y, func(o Obstacle) (float64, float64) { return o.Y, o.Y + o.H }) {
				t.Errorf("горизонталь y=%v проходит через всю карту без укрытия", y)
			}
		}
		for _, x := range xs {
			if !lineBlocked(m.Obstacles, x, func(o Obstacle) (float64, float64) { return o.X, o.X + o.W }) {
				t.Errorf("вертикаль x=%v проходит через всю карту без укрытия", x)
			}
		}
	})
}

// Щель между ящиками — линия прострела, но не проход: уже диаметра игрока,
// шире диаметра пули; пуля вдоль щели не задевает ни один из двух ящиков,
// а игрок в щели не помещается.
func TestMapCrateSlitsShootableNotWalkable(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		w := NewWorld(Config{Map: m, PlayerRadius: mapPlayerRadius, BulletRadius: mapBulletRadius})
		for _, s := range crateSlits(m) {
			if g := s.width(); g >= 2*mapPlayerRadius-mapEps || g <= 2*mapBulletRadius+mapEps {
				t.Errorf("щель %.1f px между %+v и %+v: нужно между %v и %v",
					g, s.A, s.B, 2*mapBulletRadius, 2*mapPlayerRadius)
				continue
			}
			for _, c := range []float64{s.Lo + mapBulletRadius + 0.5, s.mid(), s.Hi - mapBulletRadius - 0.5} {
				from, to := Vec2{X: s.From - 50, Y: c}, Vec2{X: s.To + 50, Y: c}
				if !s.Horizontal {
					from, to = Vec2{X: c, Y: s.From - 50}, Vec2{X: c, Y: s.To + 50}
				}
				for _, o := range []Obstacle{s.A, s.B} {
					if _, hit := segmentRectT(from, to, o, mapBulletRadius); hit {
						t.Errorf("пуля %v → %v через щель задевает ящик %+v", from, to, o)
					}
				}
			}
			at := Vec2{X: (s.From + s.To) / 2, Y: s.mid()}
			if !s.Horizontal {
				at = Vec2{X: s.mid(), Y: (s.From + s.To) / 2}
			}
			if w.isFree(at) {
				t.Errorf("игрок помещается в щель между %+v и %+v (точка %v)", s.A, s.B, at)
			}
		}
	})
}

// На арене вместо колонн — четыре пары ящиков с щелью 16 px.
func TestArenaCratePairs(t *testing.T) {
	m := ArenaMap()
	crates := 0
	for _, o := range m.Obstacles {
		if o.KindOrDefault() == ObstacleCrate {
			crates++
			if !near(o.W, 32) || !near(o.H, 32) {
				t.Errorf("ящик %+v не 32×32", o)
			}
		}
	}
	slits := crateSlits(m)
	if crates != 8 || len(slits) != 4 {
		t.Fatalf("ящиков %d, щелей %d; нужно 8 и 4", crates, len(slits))
	}
	for _, s := range slits {
		if !near(s.width(), 16) {
			t.Errorf("щель между %+v и %+v шириной %.1f, нужно 16", s.A, s.B, s.width())
		}
	}
}

// Пуля, выпущенная вдоль щели ящиков в мире по умолчанию, пролетает пару насквозь.
func TestBulletPassesCrateSlit(t *testing.T) {
	cfg := testConfig()
	cfg.Map = ArenaMap()
	w := NewWorld(cfg)
	s := crateSlits(cfg.Map)[0]
	if !s.Horizontal {
		t.Fatal("ожидалась горизонтальная щель")
	}
	from := Vec2{X: s.From - 30, Y: s.mid()}
	to := Vec2{X: s.To + 30, Y: s.mid()}
	if tw, hit := w.firstObstacleT(from, to); hit {
		t.Fatalf("пуля %v → %v упёрлась в препятствие при t=%.2f", from, to, tw)
	}
}

// lineBlocked — пересекает ли линия на координате v (по оси, заданной span)
// какое-либо препятствие. Касание ребра считается пересечением.
func lineBlocked(obs []Obstacle, v float64, span func(Obstacle) (float64, float64)) bool {
	for _, o := range obs {
		if lo, hi := span(o); v >= lo && v <= hi {
			return true
		}
	}
	return false
}

// freeCells — центры клеток сетки step, где помещается игрок.
func freeCells(m Map, step float64) (cells []Vec2, cols, rows int, free []bool) {
	w := NewWorld(Config{Map: m, PlayerRadius: mapPlayerRadius})
	cols, rows = int(m.Width/step), int(m.Height/step)
	free = make([]bool, cols*rows)
	for r := range rows {
		for c := range cols {
			p := Vec2{X: (float64(c) + 0.5) * step, Y: (float64(r) + 0.5) * step}
			lo, hi := w.bounds()
			if p.X >= lo.X && p.X <= hi.X && p.Y >= lo.Y && p.Y <= hi.Y && w.isFree(p) {
				free[r*cols+c] = true
				cells = append(cells, p)
			}
		}
	}
	return cells, cols, rows, free
}

func TestMapCoverWithinReach(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		cells, _, _, _ := freeCells(m, 5)
		worst, at := 0.0, Vec2{}
		for _, p := range cells {
			best := math.Inf(1)
			for _, o := range m.Obstacles {
				best = math.Min(best, pointRectDist(p, o))
			}
			if best > worst {
				worst, at = best, p
			}
		}
		if worst > mapMaxCoverDist {
			t.Errorf("из точки %v до укрытия %.1f px, нужно ≤ %v", at, worst, mapMaxCoverDist)
		}
	})
}

// Проходы: между любыми двумя препятствиями и между препятствием и краем —
// не уже mapMinPassage, либо препятствие примыкает к краю вплотную (упор).
// Узких щелей, в которые игрок не пролезает, нет — а значит, нет и замкнутых
// закутков, отрезанных такими щелями.
func TestMapPassagesAreWalkable(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		for i, a := range m.Obstacles {
			for _, b := range m.Obstacles[i+1:] {
				if _, ok := crateSlit(a, b); ok {
					continue // щель ящиков — не проход, см. TestMapCrateSlitsShootableNotWalkable
				}
				if d := rectDist(a, b); d < mapMinPassage-mapEps {
					t.Errorf("проход %.1f px между %+v и %+v уже %v", d, a, b, mapMinPassage)
				}
			}
			for name, gap := range map[string]float64{
				"left": a.X, "top": a.Y, "right": m.Width - a.X - a.W, "bottom": m.Height - a.Y - a.H,
			} {
				if gap > mapEps && gap < mapMinPassage-mapEps {
					t.Errorf("щель %.1f px между %+v и краем %s: ни прохода, ни примыкания", gap, a, name)
				}
			}
		}
	})
}

// Связность: из любого свободного места можно дойти до любого другого
// (заливка по сетке 4 px с 8 соседями).
func TestMapFreeSpaceConnected(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		cells, cols, rows, free := freeCells(m, 4)
		if len(cells) == 0 {
			t.Fatal("на карте нет свободного места")
		}
		start := -1
		for i, f := range free {
			if f {
				start = i
				break
			}
		}
		seen := make([]bool, len(free))
		stack := []int{start}
		seen[start] = true
		n := 0
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n++
			r, c := i/cols, i%cols
			for dr := -1; dr <= 1; dr++ {
				for dc := -1; dc <= 1; dc++ {
					rr, cc := r+dr, c+dc
					if rr < 0 || rr >= rows || cc < 0 || cc >= cols {
						continue
					}
					j := rr*cols + cc
					if free[j] && !seen[j] {
						seen[j] = true
						stack = append(stack, j)
					}
				}
			}
		}
		if n != len(cells) {
			t.Errorf("достижимо %d из %d свободных клеток: есть отрезанные области", n, len(cells))
		}
	})
}

func TestMapPickupZonesHaveRoom(t *testing.T) {
	forEachMap(t, func(t *testing.T, m Map) {
		w := NewWorld(Config{Map: m, PickupRadius: 10, Seed: 1})
		margin := DefaultConfig().PickupEdgeMargin
		for typ, zones := range m.PickupZones {
			for i, z := range zones {
				// Зона целиком не ближе PickupEdgeMargin к краю: иначе часть её
				// площади отбрасывается при спавне и зона фактически меньше.
				x0, y0, x1, y1 := z.X, z.Y, z.X+z.W, z.Y+z.H
				if z.R > 0 {
					x0, y0, x1, y1 = z.X-z.R, z.Y-z.R, z.X+z.R, z.Y+z.R
				}
				if x0 < margin-mapEps || y0 < margin-mapEps || x1 > m.Width-margin+mapEps || y1 > m.Height-margin+mapEps {
					t.Errorf("%s зона %d %+v ближе %v к краю карты", typ, i, z, margin)
				}
				ok := 0
				for range 200 {
					p := z.sample(w.rng)
					if !z.contains(p) {
						t.Fatalf("%s зона %d: точка %v вне своей зоны", typ, i, p)
					}
					if w.circleFree(p, 10) {
						ok++
					}
				}
				// Хотя бы четверть зоны свободна — иначе спавн будет часто промахиваться.
				if ok < 50 {
					t.Errorf("%s зона %d %+v: свободно лишь %d из 200 точек", typ, i, z, ok)
				}
			}
		}
	})
}

// Зоны карты по умолчанию соответствуют замыслу: аптечки в круге 150 от центра,
// патроны у боковых краёв, ускорение в углах.
func TestArenaPickupZonesPlacement(t *testing.T) {
	m := ArenaMap()
	c := Vec2{X: m.Width / 2, Y: m.Height / 2}
	for _, z := range m.PickupZones[PickupHealth] {
		if z.R <= 0 || math.Hypot(z.X-c.X, z.Y-c.Y)+z.R > 150+mapEps {
			t.Errorf("зона аптечек %+v выходит за круг 150 от центра", z)
		}
	}
	for _, z := range m.PickupZones[PickupAmmo] {
		if z.R > 0 || (z.X+z.W > 120 && z.X < m.Width-120) || z.Y+z.H < m.Height/2-100 || z.Y > m.Height/2+100 {
			t.Errorf("зона патронов %+v не у боковой стены", z)
		}
	}
	for _, z := range m.PickupZones[PickupSpeed] {
		inX := z.X+z.W <= 100 || z.X >= m.Width-100
		inY := z.Y+z.H <= 100 || z.Y >= m.Height-100
		if z.R > 0 || !inX || !inY {
			t.Errorf("зона ускорения %+v не в углу", z)
		}
	}
	for _, typ := range []PickupType{PickupHealth, PickupAmmo, PickupSpeed} {
		if len(m.PickupZones[typ]) == 0 {
			t.Errorf("у типа %s нет зон", typ)
		}
	}
}

// Пикапы в мире появляются только в зонах своего типа.
func TestPickupsSpawnInTheirZones(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 7
	cfg.PickupInterval, cfg.MaxPickups = 0.05, 100
	cfg.AmmoPickupInterval, cfg.MaxAmmoPickups = 0.05, 100
	w := NewWorld(cfg)
	tickN(w, 400)
	seen := map[PickupType]int{}
	for _, pk := range w.pickups {
		seen[pk.Type]++
		in := false
		for _, z := range cfg.PickupZones[pk.Type] {
			if z.contains(pk.Pos) {
				in = true
				break
			}
		}
		if !in {
			t.Errorf("пикап %s в %v вне своих зон", pk.Type, pk.Pos)
		}
	}
	for _, typ := range []PickupType{PickupHealth, PickupAmmo, PickupSpeed} {
		if seen[typ] == 0 {
			t.Errorf("пикапы %s не появились: %v", typ, seen)
		}
	}
}

// Восемь вошедших игроков встают на восемь разных спавнов карты.
func TestPlayersJoinAtMapSpawns(t *testing.T) {
	cfg := DefaultConfig()
	w := NewWorld(cfg)
	used := map[Vec2]PlayerID{}
	for id := PlayerID(1); id <= 8; id++ {
		w.AddPlayer(id)
		p := w.players[id].Pos
		if prev, dup := used[p]; dup {
			t.Fatalf("игроки %d и %d на одном спавне %v", prev, id, p)
		}
		used[p] = id
		if !isMapSpawn(cfg.Map, p) {
			t.Fatalf("игрок %d появился в %v — не на спавне карты", id, p)
		}
	}
}

// Респаун — на спавне карты, не ближе RespawnMinKillerDist к убийце.
func TestRespawnAtMapSpawnAwayFromKiller(t *testing.T) {
	cfg := DefaultConfig()
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	for _, killerAt := range cfg.Spawns {
		w.players[1].Pos = killerAt
		for i := range 20 {
			p := w.respawnPoint(2, 1)
			if !isMapSpawn(cfg.Map, p) {
				t.Fatalf("респаун в %v — не на спавне карты", p)
			}
			if d := math.Hypot(p.X-killerAt.X, p.Y-killerAt.Y); d < cfg.RespawnMinKillerDist {
				t.Fatalf("попытка %d: респаун %v в %.1f от убийцы %v", i, p, d, killerAt)
			}
		}
	}
}

func isMapSpawn(m Map, p Vec2) bool {
	for _, s := range m.Spawns {
		if s == p {
			return true
		}
	}
	return false
}

// Карта в текстовом виде — для отладки: go test -run TestPrintMap -v.
// Клетка 10×10 px; символ — по тому, что лежит в центре клетки:
// '#' — стена, 'x' — ящик, 'S' — спавн. Клетка мельче щели между ящиками
// (16 px), поэтому щель видна.
func TestPrintMap(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("только с -v")
	}
	m := ArenaMap()
	const step = 10
	for y := step / 2; y < int(m.Height); y += step {
		line := make([]byte, 0, int(m.Width)/step)
		for x := step / 2; x < int(m.Width); x += step {
			p := Vec2{X: float64(x), Y: float64(y)}
			ch := byte('.')
			for _, o := range m.Obstacles {
				if p.X > o.X && p.X < o.X+o.W && p.Y > o.Y && p.Y < o.Y+o.H {
					ch = '#'
					if o.KindOrDefault() == ObstacleCrate {
						ch = 'x'
					}
				}
			}
			for _, s := range m.Spawns {
				if math.Abs(s.X-p.X) < step/2 && math.Abs(s.Y-p.Y) < step/2 {
					ch = 'S'
				}
			}
			line = append(line, ch)
		}
		t.Log(string(line))
	}
}
