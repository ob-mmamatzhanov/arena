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
	Heat     float64 // перегрев ствола [0, 1]: определяет разброс выстрелов
	HP       int
	Score    int
	LastSeq  uint32 // seq последнего применённого ввода — для client-side reconciliation

	speedLeft float64 // секунд ускорения от пикапа speed
}

// PlayerState — неизменяемая копия состояния игрока для снапшота.
type PlayerState struct {
	ID      PlayerID
	Pos     Vec2
	Angle   float64
	Heat    float64
	HP      int
	Score   int
	Boosted bool // действует ускорение от пикапа
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

// Phase — фаза раунда.
type Phase string

const (
	PhasePlay         Phase = "play"         // идёт бой
	PhaseIntermission Phase = "intermission" // пауза между раундами: мир заморожен
)

// ScoreEntry — счёт игрока в итогах раунда.
type ScoreEntry struct {
	ID    PlayerID
	Score int
}

// RoundState — состояние раунда для снапшота.
type RoundState struct {
	Phase    Phase
	TimeLeft float64 // секунд до конца текущей фазы
	// WinnerID и Scores заполняются только в PhaseIntermission.
	// WinnerID = 0 — победителя нет (ничья или ни одного фрага).
	WinnerID PlayerID
	Scores   []ScoreEntry // по возрастанию ID
}

// Snapshot — полное состояние мира на конкретный тик.
// Hits содержит только попадания, случившиеся в этом тике.
type Snapshot struct {
	Tick    uint64
	Players []PlayerState
	Bullets []BulletState
	Hits    []Hit
	Pickups []Pickup // по возрастанию ID
	Round   RoundState
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

	// Отдача: каждый выстрел добавляет HeatPerShot (heat ≤ 1), каждую секунду
	// без выстрела снимается HeatDecay. Отклонение пули от Angle — равномерно
	// в ±heat×MaxSpread радиан.
	HeatPerShot float64
	HeatDecay   float64 // в секунду
	MaxSpread   float64 // радианы при heat = 1

	// Obstacles — статические препятствия карты, непроходимые для игроков и пуль.
	// Срез не меняется после создания мира.
	Obstacles []Obstacle

	// Раунды. RoundTime ≤ 0 отключает лимит времени, KillLimit ≤ 0 — лимит фрагов
	// (оба отключены — раунд бесконечен).
	RoundTime        float64 // длительность раунда, секунды
	KillLimit        int     // счёт, при достижении которого раунд заканчивается
	IntermissionTime float64 // пауза между раундами, секунды

	// Пикапы. PickupInterval ≤ 0 отключает их появление.
	PickupInterval      float64 // секунд между появлениями
	MaxPickups          int     // одновременно на карте
	PickupRadius        float64
	PickupMinPlayerDist float64 // минимальное расстояние от центра игрока при спавне
	HealthAmount        int     // hp от пикапа health
	SpeedMultiplier     float64 // множитель скорости от пикапа speed
	SpeedDuration       float64 // секунды

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
		HeatPerShot: 0.2, HeatDecay: 0.5, MaxSpread: 0.25,
		Obstacles: DefaultObstacles(),
		RoundTime: 120, KillLimit: 10, IntermissionTime: 8,
		PickupInterval: 20, MaxPickups: 3, PickupRadius: 10, PickupMinPlayerDist: 40,
		HealthAmount: 50, SpeedMultiplier: 1.6, SpeedDuration: 5,
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

	phase    Phase
	timeLeft float64  // до конца текущей фазы
	winner   PlayerID // победитель завершённого раунда (в intermission)

	pickups     []Pickup
	nextPickup  PickupID
	pickupTimer float64 // секунд до следующего появления пикапа
}

// NewWorld создаёт пустой мир; первый раунд начинается сразу. Случайность
// берётся только из генератора, засеянного cfg.Seed, поэтому симуляция воспроизводима.
func NewWorld(cfg Config) *World {
	return &World{
		cfg:         cfg,
		players:     make(map[PlayerID]*Player),
		rng:         rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)),
		phase:       PhasePlay,
		timeLeft:    max(cfg.RoundTime, 0),
		pickupTimer: cfg.PickupInterval,
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

// AddPlayer добавляет игрока в детерминированную точку спавна. Игрок входит
// в текущий раунд (в любой фазе) с нулевым счётом.
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
//
// В intermission мир заморожен: вводы отбрасываются целиком (LastSeq не растёт),
// игроки и пули стоят, идёт только таймер паузы; по его истечении — новый раунд.
func (w *World) Update(dt float64, inputs map[PlayerID]Input) {
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return
	}
	w.hits = w.hits[:0]
	if w.phase == PhaseIntermission {
		w.timeLeft = max(w.timeLeft-dt, 0)
		if w.timeLeft <= timeEps {
			w.startRound()
		}
		w.tick++
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
		speed := w.cfg.PlayerSpeed * w.speedFactor(p)
		p.Pos = w.move(p.Pos, Vec2{X: p.Move.X * speed * dt, Y: p.Move.Y * speed * dt})
	}

	// Порядок по ID — чтобы результат не зависел от порядка обхода map.
	ids := w.sortedIDs()
	w.updatePickups(dt, ids)
	w.updateBullets(dt, ids)
	w.fire(dt, ids)
	w.advanceRound(dt, ids)
	w.tick++
}

// advanceRound ведёт таймер раунда и завершает раунд по времени или лимиту фрагов.
// Проверяется после попаданий, так что фраг, достигший лимита, засчитывается в этом же тике.
func (w *World) advanceRound(dt float64, ids []PlayerID) {
	timeUp := false
	if w.cfg.RoundTime > 0 {
		w.timeLeft = max(w.timeLeft-dt, 0)
		timeUp = w.timeLeft <= timeEps
	}
	if timeUp || w.killLimitReached(ids) {
		w.endRound(ids)
	}
}

func (w *World) killLimitReached(ids []PlayerID) bool {
	if w.cfg.KillLimit <= 0 {
		return false
	}
	for _, id := range ids {
		if w.players[id].Score >= w.cfg.KillLimit {
			return true
		}
	}
	return false
}

// endRound фиксирует победителя и переходит в intermission.
// Победитель — единственный игрок с наибольшим счётом; при ничьей или
// нулевом счёте победителя нет (0).
func (w *World) endRound(ids []PlayerID) {
	w.winner = 0
	best, tie := 0, false
	for _, id := range ids {
		switch s := w.players[id].Score; {
		case s > best:
			w.winner, best, tie = id, s, false
		case s == best && s > 0:
			tie = true
		}
	}
	if tie {
		w.winner = 0
	}
	w.phase = PhaseIntermission
	w.timeLeft = max(w.cfg.IntermissionTime, 0)
	if w.timeLeft <= timeEps {
		w.startRound() // паузы нет — сразу новый раунд
	}
}

// startRound начинает новый раунд: у всех полное HP и нулевой счёт, все
// респавнятся в случайных свободных точках, пули, пикапы и вооружение сбрасываются.
func (w *World) startRound() {
	w.phase = PhasePlay
	w.timeLeft = max(w.cfg.RoundTime, 0)
	w.winner = 0
	clear(w.bullets)
	w.bullets = w.bullets[:0]
	w.hits = w.hits[:0]
	w.resetPickups()
	for _, id := range w.sortedIDs() { // порядок по ID — детерминированный rng
		p := w.players[id]
		p.HP = StartHP
		p.Score = 0
		p.Pos = w.respawnPoint(id)
		p.Move = Vec2{}
		p.Fire = false
		p.cooldown = 0
		p.Heat = 0
		p.speedLeft = 0
	}
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
	return w.circleFree(p, w.cfg.PlayerRadius)
}

// circleFree — то же для круга произвольного радиуса (например, пикапа).
func (w *World) circleFree(p Vec2, radius float64) bool {
	r := radius - contactEps
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
//
// Разброс берётся от heat до выстрела: первый выстрел с холодного ствола точен.
// В тиках без выстрела (огонь отпущен или кулдаун) heat остывает.
// Игроки обходятся по ID, поэтому последовательность rng детерминирована.
func (w *World) fire(dt float64, ids []PlayerID) {
	for _, id := range ids {
		p := w.players[id]
		p.cooldown = math.Max(0, p.cooldown-dt)
		if !p.Fire || p.cooldown > timeEps {
			p.Heat = max(p.Heat-w.cfg.HeatDecay*dt, 0)
			continue
		}
		angle := p.Angle + w.spread(p.Heat)
		p.Heat = min(p.Heat+w.cfg.HeatPerShot, 1)
		dir := Vec2{X: math.Cos(angle), Y: math.Sin(angle)}
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

// spread — случайное отклонение угла в ±heat×MaxSpread из генератора мира.
// При нулевом разбросе генератор не трогается: точный выстрел не сдвигает
// последовательность rng.
func (w *World) spread(heat float64) float64 {
	limit := heat * w.cfg.MaxSpread
	if limit <= 0 {
		return 0
	}
	return (2*w.rng.Float64() - 1) * limit
}

// Snapshot возвращает копию состояния, отсортированную по ID (детерминированно).
func (w *World) Snapshot() Snapshot {
	ps := make([]PlayerState, 0, len(w.players))
	for _, p := range w.players {
		ps = append(ps, PlayerState{
			ID:      p.ID,
			Pos:     p.Pos,
			Angle:   p.Angle,
			Heat:    p.Heat,
			HP:      p.HP,
			Score:   p.Score,
			Boosted: p.speedLeft > timeEps,
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
	round := RoundState{Phase: w.phase, TimeLeft: w.timeLeft}
	if w.phase == PhaseIntermission {
		round.WinnerID = w.winner
		round.Scores = make([]ScoreEntry, len(ps))
		for i, p := range ps {
			round.Scores[i] = ScoreEntry{ID: p.ID, Score: p.Score}
		}
	}
	return Snapshot{
		Tick:    w.tick,
		Players: ps,
		Bullets: bs,
		Hits:    slices.Clone(w.hits),
		Pickups: slices.Clone(w.pickups), // ID растут при добавлении, порядок сохраняется
		Round:   round,
	}
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
