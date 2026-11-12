package game

import (
	"math"
	"math/rand/v2"
)

// Map — геометрия арены: размеры, препятствия, точки спавна и зоны пикапов.
// Логика мира читает только эти поля, поэтому новая карта — это новая функция,
// возвращающая Map, без правок в world.go и pickups.go.
type Map struct {
	Name          string
	Width, Height float64

	// Obstacles — статические препятствия, непроходимые для игроков и пуль.
	// Срез не меняется после создания мира.
	Obstacles []Obstacle

	// Spawns — точки появления. Пусто — старое поведение: вход на окружности
	// вокруг центра, респаун в случайной свободной точке.
	Spawns []Vec2

	// PickupZones — где может появиться пикап данного типа. Тип без зон
	// появляется в любой свободной точке карты.
	PickupZones map[PickupType][]Zone
}

// Obstacle — прямоугольник карты: левый верхний угол (X, Y), ширина W, высота H.
// Kind — вид препятствия для клиента (отрисовка); на симуляцию не влияет.
// Пустой Kind означает ObstacleBrick.
type Obstacle struct {
	X, Y, W, H float64
	Kind       ObstacleKind
}

// ObstacleKind — вид препятствия.
type ObstacleKind string

const (
	ObstacleBrick ObstacleKind = "brick" // стена, вид по умолчанию
	ObstacleCrate ObstacleKind = "crate" // ящик
)

// KindOrDefault — вид препятствия с подстановкой ObstacleBrick вместо пустого.
func (o Obstacle) KindOrDefault() ObstacleKind {
	if o.Kind == "" {
		return ObstacleBrick
	}
	return o.Kind
}

// Zone — область карты: круг (R > 0, центр X, Y) или прямоугольник (X, Y, W, H).
type Zone struct {
	X, Y, W, H, R float64
}

// CircleZone — круг радиуса r с центром (cx, cy).
func CircleZone(cx, cy, r float64) Zone { return Zone{X: cx, Y: cy, R: r} }

// RectZone — прямоугольник с левым верхним углом (x, y).
func RectZone(x, y, w, h float64) Zone { return Zone{X: x, Y: y, W: w, H: h} }

// sample — равномерно распределённая точка зоны.
func (z Zone) sample(rng *rand.Rand) Vec2 {
	if z.R > 0 {
		a := 2 * math.Pi * rng.Float64()
		r := z.R * math.Sqrt(rng.Float64()) // sqrt — равномерно по площади, а не к центру
		return Vec2{X: z.X + r*math.Cos(a), Y: z.Y + r*math.Sin(a)}
	}
	return Vec2{X: z.X + rng.Float64()*z.W, Y: z.Y + rng.Float64()*z.H}
}

// contains — лежит ли точка в зоне (границы включительно).
func (z Zone) contains(p Vec2) bool {
	if z.R > 0 {
		return math.Hypot(p.X-z.X, p.Y-z.Y) <= z.R
	}
	return p.X >= z.X && p.X <= z.X+z.W && p.Y >= z.Y && p.Y <= z.Y+z.H
}

// DefaultMap — карта по умолчанию.
func DefaultMap() Map { return ArenaMap() }

// LargeMapMinPlayers — с какой вместимости комнаты берётся ArenaLargeMap:
// на разреженной арене 5–8 игрокам негде укрыться, бой сводится к перестрелке
// в открытую.
const LargeMapMinPlayers = 5

// LargeMapCrateHits — прочность ящиков на ArenaLargeMap. В бою 5–8 игроков
// ящики с обычной прочностью (5) разлетались за первые ~40 с раунда, и
// добавленные укрытия переставали работать.
const LargeMapCrateHits = 10

// MapForPlayers — карта для комнаты вместимостью maxPlayers.
func MapForPlayers(maxPlayers int) Map {
	if maxPlayers >= LargeMapMinPlayers {
		return ArenaLargeMap()
	}
	return ArenaMap()
}

// ConfigForPlayers — DefaultConfig для комнаты вместимостью maxPlayers: карта
// по вместимости (MapForPlayers), на большой арене — прочные ящики
// (LargeMapCrateHits).
func ConfigForPlayers(maxPlayers int) Config {
	cfg := DefaultConfig()
	cfg.Map = MapForPlayers(maxPlayers)
	if maxPlayers >= LargeMapMinPlayers {
		cfg.CrateHits = LargeMapCrateHits
	}
	return cfg
}

// ArenaLargeMap — арена для 5–8 игроков: та же ArenaMap (спавны, края,
// центр, пары ящиков) плюс укрытия в пустых полосах между центром и углами —
// четыре низкие стены 64×16 над и под центром и по ящику в каждой четверти
// у боковых краёв. Укрытия симметричны поворотом на 180°, проходы между ними
// не уже 32 px, спавны остаются за угловыми стенами (проверяет maps_test.go).
//
// Патронам — ещё по зоне у каждого нового ящика: при 8 игроках поток ammo
// выдаёт до 8 коробок (см. Config.AmmoBasePlayers), в две зоны у боковых
// стенок они не помещаются.
func ArenaLargeMap() Map {
	m := ArenaMap()
	m.Name = "arena-large"
	m.Obstacles = append(m.Obstacles,
		// Низкие стены над и под центром: укрытие на линиях, где раньше был пустырь.
		Obstacle{X: 268, Y: 160, W: 64, H: 16},
		Obstacle{X: 468, Y: 424, W: 64, H: 16},
		Obstacle{X: 468, Y: 160, W: 64, H: 16},
		Obstacle{X: 268, Y: 424, W: 64, H: 16},

		// Ящики в четвертях у боковых краёв, между угловой стеной и боковой стенкой.
		Obstacle{X: 140, Y: 180, W: 32, H: 32, Kind: ObstacleCrate}, // ↖
		Obstacle{X: 628, Y: 388, W: 32, H: 32, Kind: ObstacleCrate}, // ↘
		Obstacle{X: 628, Y: 180, W: 32, H: 32, Kind: ObstacleCrate}, // ↗
		Obstacle{X: 140, Y: 388, W: 32, H: 32, Kind: ObstacleCrate}, // ↙
	)
	zones := map[PickupType][]Zone{}
	for typ, z := range m.PickupZones {
		zones[typ] = append([]Zone(nil), z...)
	}
	// Патроны — ещё и с внутренней стороны каждого нового ящика.
	zones[PickupAmmo] = append(zones[PickupAmmo],
		RectZone(185, 175, 50, 50), RectZone(565, 375, 50, 50),
		RectZone(565, 175, 50, 50), RectZone(185, 375, 50, 50),
	)
	m.PickupZones = zones
	return m
}

// ArenaMap — разреженная арена 800×600 для дальних перестрелок, симметричная
// поворотом на 180° вокруг (400, 300): каждому препятствию (x, y, w, h, kind)
// соответствует (800−x−w, 600−y−h, w, h, kind), каждому спавну (x, y) —
// (800−x, 600−y). Принципы (проверяются в maps_test.go):
//   - укрытие рядом: из любой проходимой точки сетки 40 px ближайшее
//     препятствие не дальше 140 px; сквозные линии через всю карту допустимы;
//   - между препятствиями и от препятствий до края — проход не уже 32 px
//     (диаметр игрока 24 + запас) либо препятствие примыкает к краю вплотную,
//     поэтому закрытых карманов с одним выходом нет. Исключение — щель между
//     двумя выровненными ящиками: она уже диаметра игрока (не проход и не
//     карман), но шире пули, то есть только линия прострела;
//   - спавны за угловыми стенами относительно центра (линия из центра карты
//     упирается в стену), не ближе 60 px к препятствиям.
//
// Пары ящиков стоят только в ↖ и ↘ четвертях, поэтому зеркальной симметрии
// слева направо нет — только поворотная.
func ArenaMap() Map {
	const w, h = 800, 600
	return Map{
		Name:  "arena",
		Width: w, Height: h,
		Obstacles: []Obstacle{
			// Центр — главное укрытие.
			{X: 350, Y: 260, W: 100, H: 80},

			// Две пары ящиков 32×32 у центра, одна над другой с щелью 16 px:
			// игрок (диаметр 24) в щель не пролезает, а пуля (радиус 3) пролетает.
			{X: 262, Y: 216, W: 32, H: 32, Kind: ObstacleCrate}, // ← верхний
			{X: 506, Y: 352, W: 32, H: 32, Kind: ObstacleCrate},
			{X: 262, Y: 264, W: 32, H: 32, Kind: ObstacleCrate}, // ← нижний
			{X: 506, Y: 304, W: 32, H: 32, Kind: ObstacleCrate},

			// Угловые стены 100×16: за ними спавны. Длина задаёт ширину тени из центра,
			// а значит — насколько далеко друг от друга могут стоять два спавна угла.
			{X: 100, Y: 100, W: 100, H: 16}, // ↖
			{X: 600, Y: 484, W: 100, H: 16}, // ↘
			{X: 600, Y: 100, W: 100, H: 16}, // ↗
			{X: 100, Y: 484, W: 100, H: 16}, // ↙

			// Боковые стенки у левого/правого края.
			{X: 40, Y: 270, W: 16, H: 60},
			{X: 744, Y: 270, W: 16, H: 60},

			// Стенки у верхнего/нижнего края по центру.
			{X: 384, Y: 48, W: 32, H: 16},
			{X: 384, Y: 536, W: 32, H: 16},
		},
		// По две за каждой угловой стеной: одна ближе к верхнему/нижнему
		// краю, другая — к боковому, итого по две на каждую сторону карты.
		Spawns: []Vec2{
			{X: 120, Y: 36}, {X: 680, Y: 564}, // верх ↖ / низ ↘
			{X: 680, Y: 36}, {X: 120, Y: 564}, // верх ↗ / низ ↙
			{X: 28, Y: 60}, {X: 772, Y: 540}, // лево ↖ / право ↘
			{X: 772, Y: 60}, {X: 28, Y: 540}, // право ↗ / лево ↙
		},
		PickupZones: map[PickupType][]Zone{
			// Аптечки — только в центральной зоне.
			PickupHealth: {CircleZone(w/2, h/2, 150)},
			// Патроны — у боковых упоров.
			PickupAmmo: {RectZone(55, 240, 40, 120), RectZone(705, 240, 40, 120)},
			// Ускорение — в углах, целиком не ближе PickupEdgeMargin (50) к краю:
			// иначе часть зоны была бы недоступна для спавна.
			PickupSpeed: {
				RectZone(50, 50, 50, 50), RectZone(700, 50, 50, 50),
				RectZone(50, 500, 50, 50), RectZone(700, 500, 50, 50),
			},
		},
	}
}
