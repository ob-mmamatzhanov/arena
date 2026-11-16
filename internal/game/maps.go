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

	// WeaponSpots — точки, где появляются пикапы оружия (центр и открытые
	// зоны карты). Пусто — оружия на карте нет.
	WeaponSpots []Vec2
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

// LargeMapMinPlayers — с какой вместимости комнаты берётся ArenaWideMap:
// на арене 800×600 5–8 игрокам тесно и негде укрыться.
const LargeMapMinPlayers = 5

// LargeMapCrateHits — прочность ящиков на ArenaWideMap. В бою 5–8 игроков
// ящики с обычной прочностью (5) разлетались за первые ~40 с раунда, и
// укрытия переставали работать.
const LargeMapCrateHits = 10

// MapForPlayers — карта для комнаты вместимостью maxPlayers.
func MapForPlayers(maxPlayers int) Map {
	if maxPlayers >= LargeMapMinPlayers {
		return ArenaWideMap()
	}
	return ArenaMap()
}

// ConfigForPlayers — DefaultConfig для комнаты вместимостью maxPlayers: карта
// по вместимости (MapForPlayers), на большой карте — прочные ящики
// (LargeMapCrateHits).
func ConfigForPlayers(maxPlayers int) Config {
	cfg := DefaultConfig()
	cfg.Map = MapForPlayers(maxPlayers)
	if maxPlayers >= LargeMapMinPlayers {
		cfg.CrateHits = LargeMapCrateHits
	}
	return cfg
}

// withRotations возвращает препятствия вместе с их парами при повороте на 180°
// вокруг центра карты w×h. Препятствия, совпадающие со своей парой (центральные),
// передаются отдельно и не дублируются.
func withRotations(w, h float64, self []Obstacle, half ...Obstacle) []Obstacle {
	out := append([]Obstacle(nil), self...)
	for _, o := range half {
		out = append(out, o, Obstacle{X: w - o.X - o.W, Y: h - o.Y - o.H, W: o.W, H: o.H, Kind: o.Kind})
	}
	return out
}

// pointRotations — то же для точек (спавны, точки оружия).
func pointRotations(w, h float64, half ...Vec2) []Vec2 {
	out := make([]Vec2, 0, 2*len(half))
	for _, p := range half {
		out = append(out, p, Vec2{X: w - p.X, Y: h - p.Y})
	}
	return out
}

// zoneRotations — то же для прямоугольных и круглых зон.
func zoneRotations(w, h float64, half ...Zone) []Zone {
	out := make([]Zone, 0, 2*len(half))
	for _, z := range half {
		r := Zone{X: w - z.X - z.W, Y: h - z.Y - z.H, W: z.W, H: z.H}
		if z.R > 0 {
			r = Zone{X: w - z.X, Y: h - z.Y, R: z.R}
		}
		out = append(out, z, r)
	}
	return out
}

// ArenaWideMap — карта 1600×1200 для 5–8 игроков, обоих режимов. Симметрична
// поворотом на 180° вокруг (800, 600), как ArenaMap, и проходит те же проверки
// maps_test.go (укрытие не дальше 140 px, проходы ≥ 32 px, связность, спавны
// за укрытием от центра).
//
// Устройство — от плотного центра к редким краям:
//   - ядро [440, 1160]×[348, 852] — расстановка ArenaMap с укрытиями прежней
//     большой арены, сдвинутая на (400, 300): центральный блок, пары ящиков
//     со щелью 16 px, угловые и низкие стены — плотность как на арене 800×600;
//   - открытые зоны для дальнего боя — верхний и нижний коридоры во всю ширину
//     карты (y ≈ 48…220 и зеркально): внутри коридора укрытий нет, по краю
//     карты — упоры 16×48, с внутренней стороны — редкий ряд стен и ящиков;
//     сквозная линия прострела вдоль коридора — 1600 px;
//   - боковые полосы между ядром и левым/правым краем — редкие одиночные
//     стены и ящики;
//   - спавны — по периметру, по два на каждую сторону, за укрытием от центра.
func ArenaWideMap() Map {
	const w, h = 1600, 1200
	return Map{
		Name:  "arena-wide",
		Width: w, Height: h,
		Obstacles: withRotations(w, h,
			[]Obstacle{{X: 750, Y: 560, W: 100, H: 80}}, // центральный блок
			// Ядро: ArenaMap и укрытия большой арены, сдвинутые на (400, 300).
			Obstacle{X: 662, Y: 516, W: 32, H: 32, Kind: ObstacleCrate}, // пара ящиков у центра
			Obstacle{X: 662, Y: 564, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 500, Y: 400, W: 100, H: 16},                     // угловые стены ядра
			Obstacle{X: 1000, Y: 400, W: 100, H: 16},                    //
			Obstacle{X: 440, Y: 570, W: 16, H: 60},                      // боковая стенка ядра
			Obstacle{X: 784, Y: 348, W: 32, H: 16},                      // стенка над центром
			Obstacle{X: 668, Y: 460, W: 64, H: 16},                      // низкие стены над центром
			Obstacle{X: 868, Y: 460, W: 64, H: 16},                      //
			Obstacle{X: 540, Y: 480, W: 32, H: 32, Kind: ObstacleCrate}, // ящики в четвертях ядра
			Obstacle{X: 1028, Y: 480, W: 32, H: 32, Kind: ObstacleCrate},

			// Верхний коридор: упоры у края карты.
			Obstacle{X: 100, Y: 0, W: 16, H: 48},
			Obstacle{X: 377, Y: 0, W: 16, H: 48},
			Obstacle{X: 654, Y: 0, W: 16, H: 48},
			Obstacle{X: 930, Y: 0, W: 16, H: 48},
			Obstacle{X: 1207, Y: 0, W: 16, H: 48},
			Obstacle{X: 1484, Y: 0, W: 16, H: 48},
			Obstacle{X: 0, Y: 210, W: 48, H: 16},    // упоры у боковых краёв в концах коридора
			Obstacle{X: 1552, Y: 270, W: 48, H: 16}, //
			// Верхний коридор: внутренний ряд.
			Obstacle{X: 190, Y: 220, W: 96, H: 16},
			Obstacle{X: 500, Y: 214, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 548, Y: 214, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 752, Y: 220, W: 96, H: 16},
			Obstacle{X: 1020, Y: 214, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 1068, Y: 214, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 1314, Y: 220, W: 96, H: 16},

			// Левая полоса.
			Obstacle{X: 100, Y: 380, W: 16, H: 140},
			Obstacle{X: 260, Y: 330, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 250, Y: 560, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 250, Y: 608, W: 32, H: 32, Kind: ObstacleCrate},
			Obstacle{X: 100, Y: 760, W: 120, H: 16},
			Obstacle{X: 300, Y: 820, W: 32, H: 32, Kind: ObstacleCrate},
		),
		Spawns: pointRotations(w, h,
			Vec2{X: 240, Y: 36}, Vec2{X: 1360, Y: 36}, // верх
			Vec2{X: 36, Y: 440}, Vec2{X: 36, Y: 800}, // лево
		),
		PickupZones: map[PickupType][]Zone{
			PickupHealth: {CircleZone(w/2, h/2, 150)},
			PickupAmmo: zoneRotations(w, h,
				RectZone(470, 540, 50, 120),  // у боковой стенки ядра
				RectZone(140, 520, 60, 160),  // в боковой полосе
				RectZone(280, 100, 120, 60),  // в верхнем коридоре
				RectZone(1200, 100, 120, 60), //
			),
			PickupSpeed: zoneRotations(w, h,
				RectZone(50, 50, 50, 50), RectZone(1500, 50, 50, 50),
			),
		},
		// Оружие — над и под центральным блоком и по две точки в каждом коридоре.
		WeaponSpots: pointRotations(w, h,
			Vec2{X: 800, Y: 510},
			Vec2{X: 500, Y: 131}, Vec2{X: 1100, Y: 131},
		),
	}
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
		// Оружие — над и под центральным блоком и в открытых полосах между
		// боковыми стенками и парами ящиков.
		WeaponSpots: pointRotations(w, h, Vec2{X: 400, Y: 230}, Vec2{X: 160, Y: 300}),
	}
}
