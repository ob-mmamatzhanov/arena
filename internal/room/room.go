// Package room связывает игровой мир с сетевыми соединениями.
//
// Комната — это ровно одна горутина (Run). Всё её состояние (мир, клиенты,
// буферы ввода) принадлежит этой горутине, поэтому мьютексов нет: внешний
// мир общается с комнатой только через каналы join/leave/inputs.
// Мутации игрового мира выполняются только внутри тика.
package room

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"nhooyr.io/websocket"

	"arena/internal/game"
	anet "arena/internal/net"
)

const (
	TickRate   = 20
	MaxPlayers = 8

	tickDT       = 1.0 / TickRate
	inputsBuffer = MaxPlayers * 32
)

var (
	ErrFull   = errors.New("room: full")
	ErrClosed = errors.New("room: closed")
)

type joinResult struct {
	id  game.PlayerID
	err error
}

// Client — транспорт до игрока с точки зрения комнаты.
// Реализации обязаны не блокироваться: комната вызывает методы из своей горутины.
type Client interface {
	// TrySend ставит сообщение в очередь; false — очередь полна или клиент закрыт.
	TrySend(msg []byte) bool
	// Close закрывает соединение без блокировки. Идемпотентен.
	Close(code websocket.StatusCode, reason string)
}

type joinReq struct {
	conn  Client
	reply chan joinResult
}

// Room — игровая комната на 2–8 игроков.
type Room struct {
	name string
	log  *slog.Logger

	join   chan joinReq
	leave  chan game.PlayerID
	inputs chan anet.PlayerInput
	done   chan struct{}

	// Ниже — состояние, принадлежащее исключительно горутине Run.
	world         *game.World
	clients       map[game.PlayerID]Client
	nextID        game.PlayerID
	pendingJoins  []game.PlayerID
	pendingLeaves []game.PlayerID
	pendingInputs map[game.PlayerID]game.Input
}

// New создаёт комнату. Её нужно запустить через Run.
func New(name string, log *slog.Logger) *Room {
	cfg := game.DefaultConfig()
	cfg.Seed = uint64(time.Now().UnixNano()) // разные комнаты/запуски — разные респавны
	return &Room{
		name:          name,
		log:           log.With("room", name),
		join:          make(chan joinReq),
		leave:         make(chan game.PlayerID, MaxPlayers),
		inputs:        make(chan anet.PlayerInput, inputsBuffer),
		done:          make(chan struct{}),
		world:         game.NewWorld(cfg),
		clients:       make(map[game.PlayerID]Client, MaxPlayers),
		pendingInputs: make(map[game.PlayerID]game.Input, MaxPlayers),
	}
}

// Name возвращает имя комнаты.
func (r *Room) Name() string { return r.name }

// Done закрывается после остановки комнаты.
func (r *Room) Done() <-chan struct{} { return r.done }

// Join регистрирует соединение в комнате и возвращает ID игрока.
// Welcome кладётся в очередь отправки соединения до возврата.
func (r *Room) Join(ctx context.Context, c Client) (uint32, error) {
	req := joinReq{conn: c, reply: make(chan joinResult, 1)}
	select {
	case r.join <- req:
	case <-r.done:
		return 0, ErrClosed
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// Комната отвечает сразу при приёме запроса, ответ буферизован.
	res := <-req.reply
	return uint32(res.id), res.err
}

// Leave сообщает комнате, что игрок отключился. Не блокирует после остановки комнаты.
func (r *Room) Leave(id uint32) {
	select {
	case r.leave <- game.PlayerID(id):
	case <-r.done:
	}
}

// PushInput передаёт ввод в комнату без блокировки.
// При переполнении ввод отбрасывается: следующий всё равно придёт через ~50 мс.
func (r *Room) PushInput(in anet.PlayerInput) bool {
	select {
	case r.inputs <- in:
		return true
	default:
		return false
	}
}

// Run — главный цикл комнаты. Блокирует до отмены ctx.
func (r *Room) Run(ctx context.Context) {
	defer close(r.done)
	defer r.closeAll()

	ticker := time.NewTicker(time.Second / TickRate)
	defer ticker.Stop()

	r.log.Info("room started", "tickRate", TickRate, "maxPlayers", MaxPlayers)
	for {
		select {
		case <-ctx.Done():
			r.log.Info("room stopping")
			return
		case req := <-r.join:
			r.handleJoin(req)
		case id := <-r.leave:
			r.handleLeave(id)
		case in := <-r.inputs:
			r.bufferInput(in)
		case <-ticker.C:
			r.tick()
		}
	}
}

// handleJoin резервирует слот и отправляет Welcome. В мир игрок попадёт на ближайшем тике.
func (r *Room) handleJoin(req joinReq) {
	if len(r.clients) >= MaxPlayers {
		req.reply <- joinResult{err: ErrFull}
		return
	}
	r.nextID++
	id := r.nextID
	r.clients[id] = req.conn
	r.pendingJoins = append(r.pendingJoins, id)

	cfg := r.world.Config()
	welcome, err := json.Marshal(anet.Welcome{
		Type:         anet.TypeWelcome,
		PlayerID:     uint32(id),
		Room:         r.name,
		TickRate:     TickRate,
		Width:        cfg.Width,
		Height:       cfg.Height,
		PlayerSpeed:  cfg.PlayerSpeed,
		PlayerRadius: cfg.PlayerRadius,
	})
	if err == nil && !req.conn.TrySend(welcome) {
		err = anet.ErrClosed
	}
	if err != nil {
		r.log.Warn("welcome failed", "player", id, "err", err)
	}
	req.reply <- joinResult{id: id}
	r.log.Info("player joined", "player", id, "clients", len(r.clients))
}

func (r *Room) handleLeave(id game.PlayerID) {
	if _, ok := r.clients[id]; !ok {
		return // уже удалён (например, дропнут как медленный)
	}
	delete(r.clients, id)
	delete(r.pendingInputs, id)
	r.pendingLeaves = append(r.pendingLeaves, id)
	r.log.Info("player left", "player", id, "clients", len(r.clients))
}

// bufferInput сохраняет самый свежий (по seq) ввод игрока до следующего тика.
// Fire накапливается по OR за тик: иначе короткое нажатие, за которым в том же
// тике пришёл ввод с Fire=false, потерялось бы.
func (r *Room) bufferInput(pi anet.PlayerInput) {
	id := game.PlayerID(pi.PlayerID)
	if _, ok := r.clients[id]; !ok {
		return
	}
	fire := pi.Input.Fire
	if prev, ok := r.pendingInputs[id]; ok {
		if pi.Input.Seq <= prev.Seq {
			return
		}
		fire = fire || prev.Fire
	}
	r.pendingInputs[id] = game.Input{
		Seq:   pi.Input.Seq,
		MoveX: pi.Input.MoveX,
		MoveY: pi.Input.MoveY,
		Angle: pi.Input.Angle,
		Fire:  fire,
	}
}

// tick — единственное место, где мутирует игровой мир.
func (r *Room) tick() {
	for _, id := range r.pendingJoins {
		r.world.AddPlayer(id)
	}
	r.pendingJoins = r.pendingJoins[:0]

	for _, id := range r.pendingLeaves {
		r.world.RemovePlayer(id)
	}
	r.pendingLeaves = r.pendingLeaves[:0]

	r.world.Update(tickDT, r.pendingInputs)
	clear(r.pendingInputs)

	if len(r.clients) == 0 {
		return
	}
	r.broadcast(r.world.Snapshot())
}

// broadcast сериализует снапшот один раз и неблокирующе раздаёт его всем.
// Клиент, чья очередь переполнена, отключается и удаляется из мира.
func (r *Room) broadcast(s game.Snapshot) {
	msg := anet.Snapshot{
		Type:    anet.TypeSnapshot,
		Tick:    s.Tick,
		Players: make([]anet.PlayerState, len(s.Players)),
		Bullets: make([]anet.BulletState, len(s.Bullets)),
		Hits:    make([]anet.Hit, len(s.Hits)),
	}
	for i, b := range s.Bullets {
		msg.Bullets[i] = anet.BulletState{
			ID:    uint32(b.ID),
			Owner: uint32(b.Owner),
			X:     b.Pos.X,
			Y:     b.Pos.Y,
			VX:    b.Vel.X,
			VY:    b.Vel.Y,
		}
	}
	for i, h := range s.Hits {
		msg.Hits[i] = anet.Hit{
			Target:  uint32(h.Target),
			Shooter: uint32(h.Shooter),
			X:       h.Pos.X,
			Y:       h.Pos.Y,
			Killed:  h.Killed,
		}
	}
	for i, p := range s.Players {
		msg.Players[i] = anet.PlayerState{
			ID:      uint32(p.ID),
			X:       p.Pos.X,
			Y:       p.Pos.Y,
			Angle:   p.Angle,
			HP:      p.HP,
			Score:   p.Score,
			LastSeq: p.LastSeq,
		}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		r.log.Error("marshal snapshot", "err", err)
		return
	}

	for id, c := range r.clients {
		if c.TrySend(data) {
			continue
		}
		r.log.Warn("dropping slow client", "player", id)
		c.Close(websocket.StatusPolicyViolation, "too slow")
		delete(r.clients, id) // удаление во время range по map допустимо
		delete(r.pendingInputs, id)
		r.world.RemovePlayer(id)
	}
}

func (r *Room) closeAll() {
	for id, c := range r.clients {
		c.Close(websocket.StatusGoingAway, "server shutting down")
		delete(r.clients, id)
	}
}
