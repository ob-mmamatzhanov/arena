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
	"math"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"

	"arena/internal/game"
	"arena/internal/leaderboard"
	anet "arena/internal/net"
)

const (
	TickRate = 20
	// MaxPlayers — верхняя граница вместимости любой комнаты; конкретная
	// комната может быть меньше (WithMaxPlayers).
	MaxPlayers = 8
	// DefaultMode — режим комнаты, если он не задан опцией WithMode.
	DefaultMode = "arena"

	tickDT       = 1.0 / TickRate
	inputsBuffer = MaxPlayers * 32

	// scoresBuffer — очередь записей в таблицу лидеров. При переполнении
	// (хранилище тормозит) записи теряются, а тик не ждёт.
	scoresBuffer = 256
	// storeTimeout — таймаут одной записи в хранилище.
	storeTimeout = 2 * time.Second
	// flushTimeout — сколько при остановке комнаты ждать дозаписи очереди.
	flushTimeout = 5 * time.Second
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
	join  anet.Join
	reply chan joinResult
}

// client — подключённый игрок: транспорт и профиль.
type client struct {
	conn Client
	profile
}

// scoreUpdate — прибавка к счёту игрока в таблице лидеров.
type scoreUpdate struct {
	key, name string
	score     int
}

// retiredSeats — значение seats у списанной комнаты (Retire): заведомо
// отрицательное, чтобы освобождение мест после списания не вернуло его к нулю.
const retiredSeats = math.MinInt32 / 2

// Room — игровая комната на 2–8 игроков.
type Room struct {
	name       string
	mode       string
	maxPlayers int
	log        *slog.Logger
	onTick     func(time.Duration) // nil — длительность тика не измеряется

	// Ниже — состояние, которое комната публикует наружу без блокировок:
	// его читают менеджер комнат и HTTP-обработчики из других горутин.
	//
	// seats — занятые и зарезервированные места (Reserve). Резерв берётся
	// снаружи CAS-ом, освобождается либо неудачным JoinReserved, либо
	// горутиной Run при уходе игрока. Отрицательное — комната списана.
	seats   atomic.Int32
	players atomic.Int32 // подключённые игроки (len(clients)); пишет только Run
	phase   atomic.Value // game.Phase; пишет только Run при смене фазы

	join   chan joinReq
	leave  chan game.PlayerID
	inputs chan anet.PlayerInput
	done   chan struct{}

	// lb — таблица лидеров (nil — фраги не записываются). В неё пишет только
	// горутина writeScores, получая записи из канала scores.
	lb     leaderboard.Store
	scores chan scoreUpdate

	// Ниже — состояние, принадлежащее исключительно горутине Run.
	world         *game.World
	clients       map[game.PlayerID]*client
	nextID        game.PlayerID
	pendingJoins  []game.PlayerID
	pendingLeaves []game.PlayerID
	pendingInputs map[game.PlayerID]game.Input
	lastPhase     game.Phase
}

// Option настраивает комнату при создании.
type Option func(*options)

type options struct {
	mode       string
	maxPlayers int
	cfg        *game.Config
	onTick     func(time.Duration)
}

// WithMode задаёт режим комнаты (по умолчанию DefaultMode). Комната только
// хранит его и сообщает клиенту в Welcome; правила режима задаются конфигом
// мира (WithGameConfig).
func WithMode(mode string) Option { return func(o *options) { o.mode = mode } }

// WithMaxPlayers ограничивает вместимость комнаты; значение приводится к 1…MaxPlayers.
func WithMaxPlayers(n int) Option { return func(o *options) { o.maxPlayers = n } }

// WithGameConfig задаёт конфиг мира вместо game.DefaultConfig(). Seed
// всё равно берётся от времени: разные комнаты — разные респавны.
func WithGameConfig(cfg game.Config) Option { return func(o *options) { o.cfg = &cfg } }

// WithTickObserver передаёт длительность каждого тика в f. f вызывается из
// горутины комнаты и не должен блокироваться.
func WithTickObserver(f func(time.Duration)) Option { return func(o *options) { o.onTick = f } }

// New создаёт комнату. Её нужно запустить через Run. Фраги игроков
// записываются в lb; nil — без таблицы лидеров.
func New(name string, lb leaderboard.Store, log *slog.Logger, opts ...Option) *Room {
	o := options{mode: DefaultMode, maxPlayers: MaxPlayers}
	for _, opt := range opts {
		opt(&o)
	}
	cfg := game.DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}
	cfg.Seed = uint64(time.Now().UnixNano()) // разные комнаты/запуски — разные респавны
	r := &Room{
		name:          name,
		mode:          o.mode,
		maxPlayers:    min(max(o.maxPlayers, 1), MaxPlayers),
		log:           log.With("room", name),
		onTick:        o.onTick,
		join:          make(chan joinReq),
		leave:         make(chan game.PlayerID, MaxPlayers),
		inputs:        make(chan anet.PlayerInput, inputsBuffer),
		done:          make(chan struct{}),
		lb:            lb,
		scores:        make(chan scoreUpdate, scoresBuffer),
		world:         game.NewWorld(cfg),
		clients:       make(map[game.PlayerID]*client, MaxPlayers),
		pendingInputs: make(map[game.PlayerID]game.Input, MaxPlayers),
	}
	r.lastPhase = r.world.Phase()
	r.phase.Store(r.lastPhase)
	return r
}

// Name возвращает имя (ID) комнаты.
func (r *Room) Name() string { return r.name }

// Mode возвращает режим комнаты.
func (r *Room) Mode() string { return r.mode }

// MaxPlayers возвращает вместимость комнаты.
func (r *Room) MaxPlayers() int { return r.maxPlayers }

// Players возвращает число подключённых игроков. Безопасен из любой горутины.
func (r *Room) Players() int { return int(r.players.Load()) }

// Seats возвращает число занятых и зарезервированных мест (0 у списанной
// комнаты). Безопасен из любой горутины.
func (r *Room) Seats() int { return max(int(r.seats.Load()), 0) }

// Phase возвращает текущую фазу раунда. Безопасен из любой горутины.
func (r *Room) Phase() game.Phase { return r.phase.Load().(game.Phase) }

// Done закрывается после остановки комнаты.
func (r *Room) Done() <-chan struct{} { return r.done }

// Reserve занимает место под будущий JoinReserved. ErrFull — мест нет,
// ErrClosed — комната списана (Retire). Неиспользованный резерв нужно
// вернуть через Unreserve.
func (r *Room) Reserve() error {
	for {
		n := r.seats.Load()
		if n < 0 {
			return ErrClosed
		}
		if int(n) >= r.maxPlayers {
			return ErrFull
		}
		if r.seats.CompareAndSwap(n, n+1) {
			return nil
		}
	}
}

// Unreserve возвращает место, занятое Reserve и не использованное в JoinReserved.
func (r *Room) Unreserve() { r.releaseSeat() }

// Retire списывает комнату, если в ней нет ни игроков, ни резервов: после
// этого Reserve всегда возвращает ErrClosed. Так менеджер удаляет пустую
// комнату без гонки с игроком, который как раз в неё заходит.
func (r *Room) Retire() bool { return r.seats.CompareAndSwap(0, retiredSeats) }

// Join занимает место (Reserve) и подключает соединение — см. JoinReserved.
func (r *Room) Join(ctx context.Context, c Client, j anet.Join) (uint32, error) {
	if err := r.Reserve(); err != nil {
		return 0, err
	}
	return r.JoinReserved(ctx, c, j)
}

// JoinReserved регистрирует соединение в комнате на месте, занятом Reserve,
// с профилем из сообщения join и возвращает ID игрока. Welcome кладётся в
// очередь отправки соединения до возврата. При ошибке место освобождается.
func (r *Room) JoinReserved(ctx context.Context, c Client, j anet.Join) (uint32, error) {
	req := joinReq{conn: c, join: j, reply: make(chan joinResult, 1)}
	select {
	case r.join <- req:
	case <-r.done:
		r.Unreserve()
		return 0, ErrClosed
	case <-ctx.Done():
		r.Unreserve()
		return 0, ctx.Err()
	}
	// Комната отвечает сразу при приёме запроса, ответ буферизован.
	res := <-req.reply
	if res.err != nil {
		r.Unreserve()
	}
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
	writerDone := make(chan struct{})
	go r.writeScores(writerDone)
	defer r.flushScores(writerDone) // после closeAll: Done закрывается, когда очередь дописана
	defer r.closeAll()

	ticker := time.NewTicker(time.Second / TickRate)
	defer ticker.Stop()

	r.log.Info("room started", "mode", r.mode, "tickRate", TickRate, "maxPlayers", r.maxPlayers)
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
	if len(r.clients) >= r.maxPlayers { // при честных резервах не случается
		req.reply <- joinResult{err: ErrFull}
		return
	}
	r.nextID++
	id := r.nextID
	cl := &client{conn: req.conn, profile: profile{
		name:  playerName(req.join.Name, id),
		key:   playerKey(req.join.PlayerKey),
		color: pickColor(r.clients), // до добавления себя
	}}
	r.clients[id] = cl
	r.players.Store(int32(len(r.clients)))
	r.pendingJoins = append(r.pendingJoins, id)

	cfg := r.world.Config()
	obstacles := make([]anet.Obstacle, len(cfg.Obstacles))
	for i, o := range cfg.Obstacles {
		obstacles[i] = anet.Obstacle{X: o.X, Y: o.Y, W: o.W, H: o.H, Kind: string(o.KindOrDefault())}
	}
	welcome, err := json.Marshal(anet.Welcome{
		Type:         anet.TypeWelcome,
		PlayerID:     uint32(id),
		Room:         r.name,
		Mode:         r.mode,
		TickRate:     TickRate,
		Name:         cl.name,
		Color:        cl.color,
		PlayerKey:    cl.key,
		Width:        cfg.Width,
		Height:       cfg.Height,
		PlayerSpeed:  cfg.PlayerSpeed,
		PlayerRadius: cfg.PlayerRadius,
		Obstacles:    obstacles,
	})
	if err == nil && !cl.conn.TrySend(welcome) {
		err = anet.ErrClosed
	}
	if err != nil {
		r.log.Warn("welcome failed", "player", id, "err", err)
	}
	req.reply <- joinResult{id: id}
	r.log.Info("player joined", "player", id, "name", cl.name, "color", cl.color, "clients", len(r.clients))
}

func (r *Room) handleLeave(id game.PlayerID) {
	if _, ok := r.clients[id]; !ok {
		return // уже удалён (например, дропнут как медленный)
	}
	r.removeClient(id)
	r.pendingLeaves = append(r.pendingLeaves, id)
	r.log.Info("player left", "player", id, "clients", len(r.clients))
}

// removeClient забывает клиента и освобождает его место. Из мира игрока
// убирает вызывающий: сразу или на следующем тике.
func (r *Room) removeClient(id game.PlayerID) {
	delete(r.clients, id)
	delete(r.pendingInputs, id)
	r.players.Store(int32(len(r.clients)))
	r.releaseSeat()
}

// releaseSeat освобождает одно место. У списанной комнаты и у клиента,
// добавленного в обход Reserve (тесты), счётчик не уходит ниже нуля.
func (r *Room) releaseSeat() {
	for {
		n := r.seats.Load()
		if n <= 0 || r.seats.CompareAndSwap(n, n-1) {
			return
		}
	}
}

// bufferInput сохраняет самый свежий (по seq) ввод игрока до следующего тика.
// Fire и Dash накапливаются по OR за тик: иначе короткое нажатие, за которым
// в том же тике пришёл ввод без него, потерялось бы.
func (r *Room) bufferInput(pi anet.PlayerInput) {
	id := game.PlayerID(pi.PlayerID)
	if _, ok := r.clients[id]; !ok {
		return
	}
	fire, dash := pi.Input.Fire, pi.Input.Dash
	if prev, ok := r.pendingInputs[id]; ok {
		if pi.Input.Seq <= prev.Seq {
			return
		}
		fire = fire || prev.Fire
		dash = dash || prev.Dash
	}
	r.pendingInputs[id] = game.Input{
		Seq:   pi.Input.Seq,
		MoveX: pi.Input.MoveX,
		MoveY: pi.Input.MoveY,
		Angle: pi.Input.Angle,
		Fire:  fire,
		Dash:  dash,
	}
}

// tick — единственное место, где мутирует игровой мир.
func (r *Room) tick() {
	if r.onTick != nil {
		start := time.Now()
		defer func() { r.onTick(time.Since(start)) }()
	}
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
	if p := r.world.Phase(); p != r.lastPhase { // Store только при смене: без аллокаций в каждом тике
		r.lastPhase = p
		r.phase.Store(p)
	}

	if len(r.clients) == 0 {
		return // без клиентов нет и стрелков, которым записывать фраги
	}
	s := r.world.Snapshot()
	r.recordKills(s.Events)
	r.broadcast(s)
}

// recordKills ставит в очередь таблицы лидеров +1 стрелку за каждый kill.
// Не блокирует: при полной очереди запись теряется с предупреждением.
// Стрелок, уже отключившийся к этому тику, не записывается — его профиля нет.
func (r *Room) recordKills(events []game.Event) {
	if r.lb == nil {
		return
	}
	for _, e := range events {
		if e.Type != game.EventKill {
			continue
		}
		cl, ok := r.clients[e.Shooter]
		if !ok {
			continue
		}
		select {
		case r.scores <- scoreUpdate{key: cl.key, name: cl.name, score: 1}:
		default:
			r.log.Warn("leaderboard queue full, kill not recorded", "player", e.Shooter)
		}
	}
}

// writeScores — единственный писатель в таблицу лидеров. Работает, пока
// канал scores не закрыт; ошибки хранилища только логируются.
func (r *Room) writeScores(done chan<- struct{}) {
	defer close(done)
	for u := range r.scores {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		err := r.lb.AddScore(ctx, u.key, u.name, u.score)
		cancel()
		if err != nil {
			r.log.Warn("leaderboard write failed", "name", u.name, "err", err)
		}
	}
}

// flushScores закрывает очередь и ждёт, пока писатель её доработает, но не
// дольше flushTimeout: недоступное хранилище не должно задерживать остановку.
func (r *Room) flushScores(writerDone <-chan struct{}) {
	close(r.scores)
	select {
	case <-writerDone:
	case <-time.After(flushTimeout):
		r.log.Warn("leaderboard flush timed out", "pending", len(r.scores))
	}
}

// broadcast сериализует снапшот один раз и неблокирующе раздаёт его всем.
// Клиент, чья очередь переполнена, отключается и удаляется из мира.
func (r *Room) broadcast(s game.Snapshot) {
	msg := anet.Snapshot{
		Type:    anet.TypeSnapshot,
		Tick:    s.Tick,
		Players: make([]anet.PlayerState, len(s.Players)),
		Bullets: make([]anet.BulletState, len(s.Bullets)),
		Pickups: make([]anet.Pickup, len(s.Pickups)),
	}
	if len(s.Events) > 0 { // nil → поле events опускается (omitempty)
		msg.Events = make([]anet.Event, len(s.Events))
		for i, e := range s.Events {
			msg.Events[i] = anet.Event{
				Type:       string(e.Type),
				X:          e.Pos.X,
				Y:          e.Pos.Y,
				TargetID:   uint32(e.Target),
				ShooterID:  uint32(e.Shooter),
				PlayerID:   uint32(e.Player),
				PickupType: string(e.Pickup),
			}
		}
	}
	for i, pk := range s.Pickups {
		msg.Pickups[i] = anet.Pickup{ID: uint32(pk.ID), Type: string(pk.Type), X: pk.Pos.X, Y: pk.Pos.Y}
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
	for i, p := range s.Players {
		pr := r.profileOf(p.ID)
		msg.Players[i] = anet.PlayerState{
			ID:      uint32(p.ID),
			Name:    pr.name,
			Color:   pr.color,
			X:       p.Pos.X,
			Y:       p.Pos.Y,
			Angle:   p.Angle,
			Heat:    p.Heat,
			HP:      p.HP,
			Score:   p.Score,
			Ammo:    p.Ammo,
			Boosted: p.Boosted,
			DashCd:  p.DashCd,
			LastSeq: p.LastSeq,

			Dead:      p.Dead,
			RespawnIn: p.RespawnIn,
			Invuln:    p.Invuln,
		}
	}
	msg.Round = anet.Round{Phase: string(s.Round.Phase), TimeLeft: s.Round.TimeLeft}
	if s.Round.Phase == game.PhaseIntermission {
		winner := uint32(s.Round.WinnerID)
		msg.Round.WinnerID = &winner
		msg.Round.Scores = make([]anet.Score, len(s.Round.Scores))
		for i, sc := range s.Round.Scores {
			msg.Round.Scores[i] = anet.Score{ID: uint32(sc.ID), Name: r.profileOf(sc.ID).name, Score: sc.Score}
		}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		r.log.Error("marshal snapshot", "err", err)
		return
	}

	for id, c := range r.clients {
		if c.conn.TrySend(data) {
			continue
		}
		r.log.Warn("dropping slow client", "player", id)
		c.conn.Close(websocket.StatusPolicyViolation, "too slow")
		r.removeClient(id) // удаление во время range по map допустимо
		r.world.RemovePlayer(id)
	}
}

// profileOf — профиль игрока; пустой, если клиента уже нет (вышел в этом тике).
func (r *Room) profileOf(id game.PlayerID) profile {
	if c, ok := r.clients[id]; ok {
		return c.profile
	}
	return profile{}
}

func (r *Room) closeAll() {
	for id, c := range r.clients {
		c.conn.Close(websocket.StatusGoingAway, "server shutting down")
		delete(r.clients, id)
	}
	r.players.Store(0)
}
