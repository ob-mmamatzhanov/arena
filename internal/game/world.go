// Package game содержит чистую игровую логику без сети и конкурентности.
// World не потокобезопасен: им владеет ровно одна горутина (комната).
package game

import (
	"math"
	"slices"
)

// PlayerID — идентификатор игрока внутри мира.
type PlayerID uint32

// Vec2 — двумерный вектор.
type Vec2 struct {
	X, Y float64
}

// Input — намерение игрока на тик. MoveX/MoveY — направление в [-1, 1].
type Input struct {
	Seq   uint32
	MoveX float64
	MoveY float64
}

// Player — состояние игрока в мире.
type Player struct {
	ID      PlayerID
	Pos     Vec2
	Move    Vec2   // текущее (последнее принятое) направление движения
	LastSeq uint32 // seq последнего применённого ввода — для client-side reconciliation
}

// PlayerState — неизменяемая копия состояния игрока для снапшота.
type PlayerState struct {
	ID      PlayerID
	Pos     Vec2
	LastSeq uint32
}

// Snapshot — полное состояние мира на конкретный тик.
type Snapshot struct {
	Tick    uint64
	Players []PlayerState
}

// Config — параметры мира.
type Config struct {
	Width, Height float64
	PlayerSpeed   float64 // единиц в секунду
	PlayerRadius  float64
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig() Config {
	return Config{Width: 1000, Height: 1000, PlayerSpeed: 200, PlayerRadius: 16}
}

// World — authoritative-состояние игры.
type World struct {
	cfg     Config
	tick    uint64
	players map[PlayerID]*Player
}

// NewWorld создаёт пустой мир.
func NewWorld(cfg Config) *World {
	return &World{cfg: cfg, players: make(map[PlayerID]*Player)}
}

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
	w.players[id] = &Player{ID: id, Pos: w.spawnPoint(id)}
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
		p.LastSeq = in.Seq
	}

	r := w.cfg.PlayerRadius
	for _, p := range w.players {
		p.Pos.X = clamp(p.Pos.X+p.Move.X*w.cfg.PlayerSpeed*dt, r, w.cfg.Width-r)
		p.Pos.Y = clamp(p.Pos.Y+p.Move.Y*w.cfg.PlayerSpeed*dt, r, w.cfg.Height-r)
	}
	w.tick++
}

// Snapshot возвращает копию состояния, отсортированную по ID (детерминированно).
func (w *World) Snapshot() Snapshot {
	ps := make([]PlayerState, 0, len(w.players))
	for _, p := range w.players {
		ps = append(ps, PlayerState{ID: p.ID, Pos: p.Pos, LastSeq: p.LastSeq})
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
	return Snapshot{Tick: w.tick, Players: ps}
}

// spawnPoint раскладывает игроков по окружности вокруг центра арены.
func (w *World) spawnPoint(id PlayerID) Vec2 {
	const slots = 8
	angle := 2 * math.Pi * float64(uint32(id)%slots) / slots
	radius := math.Min(w.cfg.Width, w.cfg.Height) / 3
	return Vec2{
		X: w.cfg.Width/2 + radius*math.Cos(angle),
		Y: w.cfg.Height/2 + radius*math.Sin(angle),
	}
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
