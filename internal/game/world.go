// Package game содержит чистую игровую логику без сети и конкурентности.
// World не потокобезопасен: им владеет ровно одна горутина (комната).
package game

import (
	"math"
	"math/rand/v2"
	"slices"
)

// PlayerID — идентификатор игрока внутри мира.
type PlayerID uint32

// Vec2 — двумерный вектор.
type Vec2 struct {
	X, Y float64
}

// StartHP — здоровье игрока при появлении в мире.
const StartHP = 100

// Input — намерение игрока на тик. MoveX/MoveY — направление в [-1, 1],
// Angle — направление взгляда в радианах.
type Input struct {
	Seq   uint32
	MoveX float64
	MoveY float64
	Angle float64
	Fire  bool // огонь зажат: стрелять по Angle, как только позволит кулдаун
}

// Player — состояние игрока в мире.
type Player struct {
	ID       PlayerID
	Pos      Vec2
	Move     Vec2    // текущее (последнее принятое) направление движения
	Angle    float64 // направление взгляда из последнего принятого ввода
	Fire     bool    // огонь зажат (из последнего принятого ввода)
	cooldown float64 // секунд до следующего возможного выстрела
	HP       int
	Score    int
	LastSeq  uint32 // seq последнего применённого ввода — для client-side reconciliation
}

// PlayerState — неизменяемая копия состояния игрока для снапшота.
type PlayerState struct {
	ID      PlayerID
	Pos     Vec2
	Angle   float64
	HP      int
	Score   int
	LastSeq uint32
}

// BulletID — идентификатор пули, уникальный в пределах мира.
type BulletID uint32

// Bullet — летящая пуля.
type Bullet struct {
	ID    BulletID
	Owner PlayerID
	Pos   Vec2
	Vel   Vec2    // единиц в секунду
	ttl   float64 // оставшееся время жизни, секунды
}

// BulletState — копия пули для снапшота.
type BulletState struct {
	ID    BulletID
	Owner PlayerID
	Pos   Vec2
	Vel   Vec2
}

// Hit — событие попадания за тик. Нужно клиенту, чтобы показать попадание:
// пуля исчезает в том же тике и в снапшот уже не попадает.
type Hit struct {
	Target  PlayerID
	Shooter PlayerID
	Pos     Vec2 // точка попадания
	Killed  bool // цель погибла и возродилась
}

// Snapshot — полное состояние мира на конкретный тик.
// Hits содержит только попадания, случившиеся в этом тике.
type Snapshot struct {
	Tick    uint64
	Players []PlayerState
	Bullets []BulletState
	Hits    []Hit
}

// Config — параметры мира.
type Config struct {
	Width, Height float64
	PlayerSpeed   float64 // единиц в секунду
	PlayerRadius  float64 // радиус тела: коллизии с краями мира и препятствиями
	HitRadius     float64 // радиус хитбокса игрока для попаданий пуль

	BulletSpeed    float64 // единиц в секунду
	BulletRadius   float64
	BulletLifetime float64 // секунды
	FireCooldown   float64 // минимальный интервал между выстрелами, секунды
	BulletDamage   int

	// Obstacles — статические препятствия карты, непроходимые для игроков и пуль.
	// Срез не меняется после создания мира.
	Obstacles []Obstacle

	// Seed инициализирует генератор мира (точки респавна). Одинаковый seed
	// и одинаковые вводы дают одинаковую симуляцию.
	Seed uint64
}

// Obstacle — прямоугольник карты: левый верхний угол (X, Y), ширина W, высота H.
type Obstacle struct {
	X, Y, W, H float64
}

// DefaultObstacles — карта 1000×1000: центральный блок, четыре угловые плиты
// и стенка. Расставлены между точками спавна (кольцо радиусом 333 вокруг
// центра) — TestDefaultSpawnPointsAreFree проверяет, что ни одна не перекрыта.
func DefaultObstacles() []Obstacle {
	return []Obstacle{
		{X: 440, Y: 440, W: 120, H: 120}, // центр
		{X: 100, Y: 100, W: 100, H: 40},  // углы
		{X: 800, Y: 100, W: 100, H: 40},
		{X: 100, Y: 860, W: 100, H: 40},
		{X: 800, Y: 860, W: 100, H: 40},
		{X: 680, Y: 300, W: 20, H: 120}, // стенка справа сверху
	}
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig() Config {
	return Config{
		Width: 1000, Height: 1000, PlayerSpeed: 200, PlayerRadius: 12, HitRadius: 12,
		BulletSpeed: 500, BulletRadius: 3, BulletLifetime: 1.5, FireCooldown: 0.2, BulletDamage: 25,
		Obstacles: DefaultObstacles(),
	}
}

// timeEps гасит ошибку накопления float при вычитании dt из таймеров:
// 0.2 - 4*0.05 может дать 5e-17 вместо 0.
const timeEps = 1e-9

// World — authoritative-состояние игры.
type World struct {
	cfg        Config
	tick       uint64
	players    map[PlayerID]*Player
	bullets    []Bullet
	nextBullet BulletID
	hits       []Hit      // попадания последнего тика
	ids        []PlayerID // переиспользуемый буфер отсортированных ID
	rng        *rand.Rand // единственный источник случайности мира
}

// NewWorld создаёт пустой мир. Случайность берётся только из генератора,
// засеянного cfg.Seed, поэтому симуляция воспроизводима.
func NewWorld(cfg Config) *World {
	return &World{
		cfg:     cfg,
		players: make(map[PlayerID]*Player),
		rng:     rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)),
	}
}

// Config возвращает параметры мира. Room берёт отсюда размеры для Welcome,
// чтобы клиент и сервер использовали один источник истины.
func (w *World) Config() Config { return w.cfg }

// Tick возвращает номер последнего выполненного тика.
func (w *World) Tick() uint64 { return w.tick }

// PlayerCount возвращает число игроков в мире.
func (w *World) PlayerCount() int { return len(w.players) }

// HasPlayer сообщает, есть ли игрок с данным ID.
func (w *World) HasPlayer(id PlayerID) bool {
	_, ok := w.players[id]
	return ok
}

// AddPlayer добавляет игрока в детерминированную точку спавна.
// Повторное добавление существующего ID ничего не делает.
func (w *World) AddPlayer(id PlayerID) {
	if _, ok := w.players[id]; ok {
		return
	}
	w.players[id] = &Player{ID: id, Pos: w.spawnPoint(id), HP: StartHP, Score: 0}
}

// RemovePlayer удаляет игрока. Идемпотентно.
func (w *World) RemovePlayer(id PlayerID) {
	delete(w.players, id)
}

// Update продвигает симуляцию на dt секунд, применяя inputs.
// Вводы для неизвестных игроков и устаревшие (seq <= LastSeq) игнорируются.
// Игрок без нового ввода продолжает двигаться по последнему направлению.
func (w *World) Update(dt float64, inputs map[PlayerID]Input) {
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return
	}
	for id, in := range inputs {
		p, ok := w.players[id]
		if !ok || in.Seq <= p.LastSeq {
			continue
		}
		p.Move = sanitizeMove(in.MoveX, in.MoveY)
		if a, ok := sanitizeAngle(in.Angle); ok {
			p.Angle = a
		}
		p.Fire = in.Fire
		p.LastSeq = in.Seq
	}

	for _, p := range w.players {
		p.Pos = w.move(p.Pos, Vec2{
			X: p.Move.X * w.cfg.PlayerSpeed * dt,
			Y: p.Move.Y * w.cfg.PlayerSpeed * dt,
		})
	}

	// Порядок по ID — чтобы результат не зависел от порядка обхода map.
	ids := w.sortedIDs()
	w.hits = w.hits[:0]
	w.updateBullets(dt, ids)
	w.fire(dt, ids)
	w.tick++
}

// contactEps допускает погрешность float при сравнении с точкой касания.
const contactEps = 1e-9

// move сдвигает игрока на d с учётом краёв мира и препятствий. Оси разрешаются
// раздельно — сначала X, затем Y (уже с новым X): упёршись в стену по одной оси,
// игрок продолжает скользить вдоль неё по другой.
func (w *World) move(pos, d Vec2) Vec2 {
	r := w.cfg.PlayerRadius
	lo, hi := w.bounds()
	if d.X != 0 {
		x := clamp(pos.X+d.X, lo.X, hi.X)
		for _, o := range w.cfg.Obstacles {
			x = sweepAxis(pos.X, x, pos.Y, o.X, o.X+o.W, o.Y, o.Y+o.H, r)
		}
		pos.X = x
	}
	if d.Y != 0 {
		y := clamp(pos.Y+d.Y, lo.Y, hi.Y)
		for _, o := range w.cfg.Obstacles {
			y = sweepAxis(pos.Y, y, pos.X, o.Y, o.Y+o.H, o.X, o.X+o.W, r)
		}
		pos.Y = y
	}
	return pos
}

// sweepAxis ограничивает движение круга радиуса r по одной оси (from → to),
// чтобы он не вошёл в прямоугольник [lo, hi] × [oLo, oHi], где первый отрезок —
// по оси движения, второй — по другой оси; other — координата центра по другой оси.
//
// Проверяется весь путь, а не только конечная точка, так что тонкую стену нельзя
// «перепрыгнуть» за тик. Касание (расстояние ровно r) не считается коллизией —
// иначе игрок, прижатый к стене, не мог бы скользить вдоль неё.
func sweepAxis(from, to, other, lo, hi, oLo, oHi, r float64) float64 {
	gap := 0.0 // расстояние от центра до прямоугольника по другой оси
	if other < oLo {
		gap = oLo - other
	} else if other > oHi {
		gap = other - oHi
	}
	if gap >= r {
		return to // круг проходит мимо прямоугольника
	}
	// На этой оси круг «выступает» за центр на reach (у углов — меньше r).
	reach := math.Sqrt(r*r - gap*gap)
	switch {
	case to > from:
		if c := lo - reach; from <= c+contactEps && to > c {
			return c
		}
	case to < from:
		if c := hi + reach; from >= c-contactEps && to < c {
			return c
		}
	}
	return to
}

// isFree сообщает, что круг игрока в точке p не пересекает ни одно препятствие.
// Касание допустимо; допуск contactEps гасит ошибку округления в точке касания,
// которую выставляет sweepAxis (иначе d² может оказаться на ulp меньше r²).
func (w *World) isFree(p Vec2) bool {
	r := w.cfg.PlayerRadius - contactEps
	for _, o := range w.cfg.Obstacles {
		dx := p.X - clamp(p.X, o.X, o.X+o.W)
		dy := p.Y - clamp(p.Y, o.Y, o.Y+o.H)
		if dx*dx+dy*dy < r*r {
			return false
		}
	}
	return true
}

// firstObstacleT возвращает параметр t∈[0, 1] самой ранней встречи пули на пути
// from → to с каким-либо препятствием.
func (w *World) firstObstacleT(from, to Vec2) (float64, bool) {
	best, found := math.Inf(1), false
	for _, o := range w.cfg.Obstacles {
		if t, ok := segmentRectT(from, to, o, w.cfg.BulletRadius); ok && t < best {
			best, found = t, true
		}
	}
	return best, found
}

// segmentRectT — пересечение отрезка from → to с прямоугольником o, расширенным
// на r (slab-метод). Скруглённые углы расширения аппроксимируются квадратными:
// при радиусе пули 3 разница в углу меньше 1.3 единицы.
func segmentRectT(from, to Vec2, o Obstacle, r float64) (float64, bool) {
	// Быстрый отсев по bounding box отрезка: почти все пули далеко от почти всех
	// препятствий, и это четыре сравнения вместо делений slab-метода.
	// Обычные сравнения, а не math.Min/Max: те не инлайнятся на amd64.
	minX, maxX := from.X, to.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := from.Y, to.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	if maxX < o.X-r || minX > o.X+o.W+r || maxY < o.Y-r || minY > o.Y+o.H+r {
		return 0, false
	}
	tmin, tmax := 0.0, 1.0
	for _, s := range [2]struct{ p, d, lo, hi float64 }{
		{from.X, to.X - from.X, o.X - r, o.X + o.W + r},
		{from.Y, to.Y - from.Y, o.Y - r, o.Y + o.H + r},
	} {
		if s.d == 0 {
			if s.p < s.lo || s.p > s.hi {
				return 0, false
			}
			continue
		}
		t1, t2 := (s.lo-s.p)/s.d, (s.hi-s.p)/s.d
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tmin, tmax = math.Max(tmin, t1), math.Min(tmax, t2)
		if tmin > tmax {
			return 0, false
		}
	}
	return tmin, true
}

// sortedIDs заполняет переиспользуемый буфер — без аллокаций в установившемся режиме.
func (w *World) sortedIDs() []PlayerID {
	w.ids = w.ids[:0]
	for id := range w.players {
		w.ids = append(w.ids, id)
	}
	slices.Sort(w.ids)
	return w.ids
}

// updateBullets двигает пули, проверяет попадания и удаляет истёкшие, вылетевшие
// и врезавшиеся в препятствия. Попадание ищется по отрезку пути за тик, а не по
// конечной точке: иначе быстрая пуля могла бы «перепрыгнуть» цель между тиками.
// Из игрока и препятствия на пути срабатывает то, что встретилось раньше.
func (w *World) updateBullets(dt float64, ids []PlayerID) {
	alive := w.bullets[:0]
	for _, b := range w.bullets {
		from := b.Pos
		to := Vec2{X: from.X + b.Vel.X*dt, Y: from.Y + b.Vel.Y*dt}
		b.ttl -= dt

		target, at, tHit, hit := w.firstHit(b.Owner, from, to, ids)
		if tWall, wall := w.firstObstacleT(from, to); wall && (!hit || tWall < tHit) {
			continue // пуля разбилась о препятствие
		}
		if hit {
			w.applyHit(b.Owner, target, at)
			continue
		}
		if b.ttl <= timeEps || to.X < 0 || to.X > w.cfg.Width || to.Y < 0 || to.Y > w.cfg.Height {
			continue
		}
		b.Pos = to
		alive = append(alive, b)
	}
	clear(w.bullets[len(alive):]) // не держим ссылок на хвост
	w.bullets = alive
}

// firstHit возвращает ближайшего к началу отрезка игрока (кроме владельца),
// которого задевает пуля на пути from→to, точку попадания и её параметр t∈[0, 1].
func (w *World) firstHit(owner PlayerID, from, to Vec2, ids []PlayerID) (*Player, Vec2, float64, bool) {
	r := w.cfg.HitRadius + w.cfg.BulletRadius
	r2 := r * r // сравниваем квадраты: без Hypot в горячем цикле
	d := Vec2{X: to.X - from.X, Y: to.Y - from.Y}
	dd := d.X*d.X + d.Y*d.Y

	var (
		best  *Player
		bestT = math.Inf(1)
		bestP Vec2
	)
	for _, id := range ids {
		if id == owner {
			continue
		}
		p := w.players[id]
		t := 0.0
		if dd > 0 {
			t = clamp(((p.Pos.X-from.X)*d.X+(p.Pos.Y-from.Y)*d.Y)/dd, 0, 1)
		}
		c := Vec2{X: from.X + d.X*t, Y: from.Y + d.Y*t}
		if dx, dy := c.X-p.Pos.X, c.Y-p.Pos.Y; dx*dx+dy*dy <= r2 && t < bestT {
			best, bestT, bestP = p, t, c
		}
	}
	return best, bestP, bestT, best != nil
}

// applyHit наносит урон. Погибшая цель сразу возрождается с полным HP
// в случайной точке, стрелок (если ещё в мире) получает очко.
func (w *World) applyHit(shooter PlayerID, target *Player, at Vec2) {
	target.HP -= w.cfg.BulletDamage
	killed := target.HP <= 0
	if killed {
		if s, ok := w.players[shooter]; ok {
			s.Score++
		}
		target.HP = StartHP
		target.Pos = w.respawnPoint(target.ID)
	}
	w.hits = append(w.hits, Hit{Target: target.ID, Shooter: shooter, Pos: at, Killed: killed})
}

// fire создаёт пули для игроков с зажатым огнём и истёкшим кулдауном.
// Пуля вылетает из позиции игрока; по владельцу она не попадает (см. firstHit).
func (w *World) fire(dt float64, ids []PlayerID) {
	for _, id := range ids {
		p := w.players[id]
		p.cooldown = math.Max(0, p.cooldown-dt)
		if !p.Fire || p.cooldown > timeEps {
			continue
		}
		dir := Vec2{X: math.Cos(p.Angle), Y: math.Sin(p.Angle)}
		w.nextBullet++
		w.bullets = append(w.bullets, Bullet{
			ID:    w.nextBullet,
			Owner: id,
			Pos:   p.Pos,
			Vel:   Vec2{X: dir.X * w.cfg.BulletSpeed, Y: dir.Y * w.cfg.BulletSpeed},
			ttl:   w.cfg.BulletLifetime,
		})
		p.cooldown = w.cfg.FireCooldown
	}
}

// Snapshot возвращает копию состояния, отсортированную по ID (детерминированно).
func (w *World) Snapshot() Snapshot {
	ps := make([]PlayerState, 0, len(w.players))
	for _, p := range w.players {
		ps = append(ps, PlayerState{
			ID:      p.ID,
			Pos:     p.Pos,
			Angle:   p.Angle,
			HP:      p.HP,
			Score:   p.Score,
			LastSeq: p.LastSeq,
		})
	}
	slices.SortFunc(ps, func(a, b PlayerState) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	bs := make([]BulletState, len(w.bullets))
	for i, b := range w.bullets {
		bs[i] = BulletState{ID: b.ID, Owner: b.Owner, Pos: b.Pos, Vel: b.Vel}
	}
	return Snapshot{Tick: w.tick, Players: ps, Bullets: bs, Hits: slices.Clone(w.hits)}
}

// spawnPoint раскладывает игроков по окружности вокруг центра арены.
func (w *World) spawnPoint(id PlayerID) Vec2 {
	const slots = 8
	angle := 2 * math.Pi * float64(uint32(id)%slots) / slots
	radius := math.Min(w.cfg.Width, w.cfg.Height) / 3
	return w.clampToWorld(Vec2{
		X: w.cfg.Width/2 + radius*math.Cos(angle),
		Y: w.cfg.Height/2 + radius*math.Sin(angle),
	})
}

// respawnAttempts — сколько случайных точек пробуем, прежде чем откатиться
// на фиксированную точку спавна. При ~5% площади под препятствиями шанс
// исчерпать попытки пренебрежимо мал.
const respawnAttempts = 64

// respawnPoint возвращает случайную свободную от препятствий точку.
// Случайность — только из генератора мира, не глобальный rand.
func (w *World) respawnPoint(id PlayerID) Vec2 {
	lo, hi := w.bounds()
	for range respawnAttempts {
		p := Vec2{
			X: lo.X + w.rng.Float64()*(hi.X-lo.X),
			Y: lo.Y + w.rng.Float64()*(hi.Y-lo.Y),
		}
		if w.isFree(p) {
			return p
		}
	}
	return w.spawnPoint(id) // свободна по построению карты (см. DefaultObstacles)
}

// bounds — допустимый диапазон центра игрока: [0, Width]×[0, Height] с отступом
// на радиус, чтобы тело не вылезало за край. Если радиус больше половины мира,
// отступ урезается — иначе нижняя граница оказалась бы за верхней.
func (w *World) bounds() (lo, hi Vec2) {
	rx := math.Min(w.cfg.PlayerRadius, w.cfg.Width/2)
	ry := math.Min(w.cfg.PlayerRadius, w.cfg.Height/2)
	return Vec2{X: rx, Y: ry}, Vec2{X: w.cfg.Width - rx, Y: w.cfg.Height - ry}
}

// clampToWorld держит позицию в пределах bounds.
func (w *World) clampToWorld(p Vec2) Vec2 {
	lo, hi := w.bounds()
	return Vec2{X: clamp(p.X, lo.X, hi.X), Y: clamp(p.Y, lo.Y, hi.Y)}
}

// sanitizeAngle нормализует угол в [-π, π]. NaN/Inf отвергаются (ok=false):
// тогда игрок сохраняет прежний угол.
func sanitizeAngle(a float64) (float64, bool) {
	if math.IsNaN(a) || math.IsInf(a, 0) {
		return 0, false
	}
	return math.Remainder(a, 2*math.Pi), true
}

// sanitizeMove отбрасывает NaN/Inf и нормализует вектор до длины не более 1,
// чтобы клиент не мог ускориться, прислав большие значения.
func sanitizeMove(x, y float64) Vec2 {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return Vec2{}
	}
	if l := math.Hypot(x, y); l > 1 {
		x, y = x/l, y/l
	}
	return Vec2{X: x, Y: y}
}

// clamp — на обычных сравнениях (math.Min/Max не инлайнятся на amd64).
// NaN проходит насквозь, как и в варианте с math.Max/Min.
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
