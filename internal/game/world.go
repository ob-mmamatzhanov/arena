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
	PlayerRadius  float64 // радиус тела: ограничивает движение у краёв мира
	HitRadius     float64 // радиус хитбокса игрока для попаданий пуль

	BulletSpeed    float64 // единиц в секунду
	BulletRadius   float64
	BulletLifetime float64 // секунды
	FireCooldown   float64 // минимальный интервал между выстрелами, секунды
	BulletDamage   int

	// Seed инициализирует генератор мира (точки респавна). Одинаковый seed
	// и одинаковые вводы дают одинаковую симуляцию.
	Seed uint64
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig() Config {
	return Config{
		Width: 1000, Height: 1000, PlayerSpeed: 200, PlayerRadius: 16, HitRadius: 12,
		BulletSpeed: 500, BulletRadius: 3, BulletLifetime: 1.5, FireCooldown: 0.2, BulletDamage: 25,
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
		p.Pos = w.clampToWorld(Vec2{
			X: p.Pos.X + p.Move.X*w.cfg.PlayerSpeed*dt,
			Y: p.Pos.Y + p.Move.Y*w.cfg.PlayerSpeed*dt,
		})
	}

	// Порядок по ID — чтобы результат не зависел от порядка обхода map.
	ids := w.sortedIDs()
	w.hits = w.hits[:0]
	w.updateBullets(dt, ids)
	w.fire(dt, ids)
	w.tick++
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

// updateBullets двигает пули, проверяет попадания и удаляет истёкшие и вылетевшие.
// Попадание ищется по отрезку пути за тик, а не по конечной точке: иначе быстрая
// пуля могла бы «перепрыгнуть» цель между тиками.
func (w *World) updateBullets(dt float64, ids []PlayerID) {
	alive := w.bullets[:0]
	for _, b := range w.bullets {
		from := b.Pos
		to := Vec2{X: from.X + b.Vel.X*dt, Y: from.Y + b.Vel.Y*dt}
		b.ttl -= dt

		if target, at, ok := w.firstHit(b.Owner, from, to, ids); ok {
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
// которого задевает пуля на пути from→to, и точку попадания.
func (w *World) firstHit(owner PlayerID, from, to Vec2, ids []PlayerID) (*Player, Vec2, bool) {
	r := w.cfg.HitRadius + w.cfg.BulletRadius
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
		if math.Hypot(c.X-p.Pos.X, c.Y-p.Pos.Y) <= r && t < bestT {
			best, bestT, bestP = p, t, c
		}
	}
	return best, bestP, best != nil
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
		target.Pos = w.randomPoint()
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

// randomPoint возвращает равномерно случайную допустимую позицию игрока.
// Использует только генератор мира, не глобальный rand.
func (w *World) randomPoint() Vec2 {
	rx := math.Min(w.cfg.PlayerRadius, w.cfg.Width/2)
	ry := math.Min(w.cfg.PlayerRadius, w.cfg.Height/2)
	return Vec2{
		X: rx + w.rng.Float64()*(w.cfg.Width-2*rx),
		Y: ry + w.rng.Float64()*(w.cfg.Height-2*ry),
	}
}

// clampToWorld держит позицию в [0, Width]×[0, Height] с отступом на радиус,
// чтобы тело игрока не вылезало за край. Если радиус больше половины мира,
// отступ урезается — иначе нижняя граница оказалась бы за верхней.
func (w *World) clampToWorld(p Vec2) Vec2 {
	rx := math.Min(w.cfg.PlayerRadius, w.cfg.Width/2)
	ry := math.Min(w.cfg.PlayerRadius, w.cfg.Height/2)
	return Vec2{
		X: clamp(p.X, rx, w.cfg.Width-rx),
		Y: clamp(p.Y, ry, w.cfg.Height-ry),
	}
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

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
