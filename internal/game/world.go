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
	Dash  bool // запрос рывка (одноразовый: действует только в тике этого ввода)
}

// Player — состояние игрока в мире.
type Player struct {
	ID       PlayerID
	Pos      Vec2
	Move     Vec2    // текущее (последнее принятое) направление движения
	Angle    float64 // направление взгляда из последнего принятого ввода
	Fire     bool    // огонь зажат (из последнего принятого ввода, пока не истёк fireLeft)
	fireLeft float64 // секунд, пока Fire без нового ввода ещё действует (см. Config.FireHoldTime)
	cooldown float64 // секунд до следующего возможного выстрела
	Heat     float64 // перегрев ствола [0, 1]: определяет разброс выстрелов
	HP       int
	Score    int
	Ammo     int    // патроны; при MaxAmmo ≤ 0 не расходуются
	LastSeq  uint32 // seq последнего применённого ввода — для client-side reconciliation

	speedLeft float64 // секунд ускорения от пикапа speed

	dashReq  bool    // в этом тике пришёл запрос рывка
	dashLeft float64 // секунд до конца текущего рывка (0 — рывка нет)
	dashDir  Vec2    // единичное направление текущего рывка
	dashCd   float64 // секунд до готовности следующего рывка

	respawnLeft float64  // > 0 — игрок мёртв, секунд до респауна
	killer      PlayerID // кто убил: респаун не ближе RespawnMinKillerDist к нему
	invulnLeft  float64  // секунд неуязвимости после появления
	// waitWave — убит зомби: мёртв без таймера, воскресает в начале следующей
	// волны (zombies.go).
	waitWave bool
}

// dead — игрок ждёт респауна: не двигается, не стреляет, вводы игнорируются,
// пули проходят сквозь него.
func (p *Player) dead() bool { return p.respawnLeft > 0 || p.waitWave }

// invulnerable — урон не наносится, но игрок может стрелять.
func (p *Player) invulnerable() bool { return p.invulnLeft > timeEps }

// PlayerState — неизменяемая копия состояния игрока для снапшота.
type PlayerState struct {
	ID      PlayerID
	Pos     Vec2
	Angle   float64
	Heat    float64
	HP      int
	Score   int
	Ammo    int
	Boosted bool    // действует ускорение от пикапа
	DashCd  float64 // секунд до готовности рывка, 0 — готов
	LastSeq uint32

	Dead      bool
	RespawnIn float64 // секунд до респауна, 0 у живого
	Invuln    bool
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

// Phase — фаза раунда.
type Phase string

const (
	PhasePlay         Phase = "play"         // идёт бой
	PhaseIntermission Phase = "intermission" // пауза между раундами: мир заморожен
	// PhaseGameOver — режим зомби: все игроки мертвы. Мир заморожен на
	// IntermissionTime, затем игра начинается заново с первой волны.
	PhaseGameOver Phase = "gameover"
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

	// Режим зомби (Zombies = true). TimeLeft в play — секунд до следующей
	// волны (0, пока волна идёт), в gameover — до новой игры.
	Zombies     bool
	Wave        int // номер текущей (или последней) волны, 0 — ещё не было
	ZombiesLeft int // живых зомби в волне
	Kills       int // убито зомби за игру
}

// Snapshot — полное состояние мира на конкретный тик.
// Events содержит только события, случившиеся в этом тике.
type Snapshot struct {
	Tick    uint64
	Players []PlayerState
	Bullets []BulletState
	Events  []Event
	Pickups []Pickup      // по возрастанию ID
	Crates  []CrateState  // повреждённые и сломанные ящики по возрастанию индекса; nil — все целые
	Zombies []ZombieState // по возрастанию ID; nil — зомби нет
	Round   RoundState
}

// Config — параметры мира.
type Config struct {
	// Map — размеры, препятствия, спавны и зоны пикапов. Встроена, поэтому
	// cfg.Width, cfg.Obstacles и т. д. доступны напрямую.
	Map

	PlayerSpeed  float64 // единиц в секунду
	PlayerRadius float64 // радиус тела: коллизии с краями мира и препятствиями
	HitRadius    float64 // радиус хитбокса игрока для попаданий пуль

	BulletSpeed    float64 // единиц в секунду
	BulletRadius   float64
	BulletLifetime float64 // секунды
	FireCooldown   float64 // минимальный интервал между выстрелами, секунды
	BulletDamage   int
	// FireHoldTime — сколько секунд Fire из ввода действует, если новых вводов
	// нет: дальше огонь считается отпущенным. Клиент шлёт ввод каждый тик, так
	// что удержание огня продлевается непрерывно, а один пропущенный тик (сетевой
	// джиттер) его не прерывает. Без лимита клик, после которого ввод замолк
	// (лаг, фоновая вкладка), стрелял бы снова по каждому истечению FireCooldown.
	// Должно быть меньше FireCooldown. ≤ 0 — Fire держится до следующего ввода.
	FireHoldTime float64

	// Отдача: каждый выстрел добавляет HeatPerShot (heat ≤ 1), каждую секунду
	// без выстрела снимается HeatDecay. Отклонение пули от Angle — равномерно
	// в ±heat×MaxSpread радиан.
	HeatPerShot float64
	HeatDecay   float64 // в секунду
	MaxSpread   float64 // радианы при heat = 1

	// Раунды. RoundTime ≤ 0 отключает лимит времени, KillLimit ≤ 0 — лимит фрагов
	// (оба отключены — раунд бесконечен).
	RoundTime        float64 // длительность раунда, секунды
	KillLimit        int     // счёт, при достижении которого раунд заканчивается
	IntermissionTime float64 // пауза между раундами, секунды

	// Пикапы health и speed (тип взвешенно). PickupInterval ≤ 0 отключает их появление.
	PickupInterval      float64 // секунд между появлениями
	MaxPickups          int     // одновременно на карте (без учёта ammo)
	PickupRadius        float64
	PickupMinPlayerDist float64 // минимальное расстояние от центра игрока при спавне
	PickupMinPickupDist float64 // минимальное расстояние между центрами пикапов (≤ 0 — без ограничения)
	PickupEdgeMargin    float64 // минимальное расстояние от центра пикапа до края мира (≤ 0 — только PickupRadius)
	HealthAmount        int     // hp от пикапа health
	SpeedMultiplier     float64 // множитель скорости от пикапа speed
	SpeedDuration       float64 // секунды

	// Патроны: StartAmmo при появлении, респауне и новом раунде, не больше
	// MaxAmmo; пикап ammo даёт AmmoAmount. MaxAmmo ≤ 0 отключает патроны —
	// стрельба не ограничена. Пикапы ammo идут отдельным потоком со своим
	// таймером и лимитом, чтобы не ждать в очереди за health/speed.
	// AmmoPickupInterval ≤ 0 отключает их появление.
	//
	// AmmoPickupInterval и MaxAmmoPickups заданы на AmmoBasePlayers игроков.
	// Когда в комнате больше игроков n, поток растёт пропорционально: интервал
	// ×base/n, лимит на карте ×n/base (вверх) — патронов на игрока столько же,
	// сколько в дуэли, и в бою 8 человек магазины не пустеют у всех сразу.
	// AmmoBasePlayers ≤ 0 отключает масштабирование.
	StartAmmo          int
	MaxAmmo            int
	AmmoAmount         int
	AmmoPickupInterval float64 // секунд между появлениями ammo
	MaxAmmoPickups     int     // ammo одновременно на карте
	AmmoBasePlayers    int     // на сколько игроков рассчитаны интервал и лимит ammo

	// Ящики (Obstacle.Kind == crate) ломаются после CrateHits попаданий пуль и
	// восстанавливаются в начале раунда (см. crates.go). CrateHits ≤ 0 — ящики
	// неразрушимы, как кирпич.
	CrateHits int

	// Рывок: импульс DashSpeed, линейно затухающий за DashDuration; повтор
	// не раньше чем через DashCooldown. DashDuration ≤ 0 отключает рывок.
	DashSpeed    float64 // единиц в секунду в начале рывка
	DashDuration float64 // секунды
	DashCooldown float64 // секунды

	// Смерть и респаун. Убитый RespawnDelay секунд мёртв, затем возрождается
	// в свободной точке не ближе RespawnMinKillerDist к убийце. RespawnDelay ≤ 0 —
	// респаун в том же тике. После появления (вход, респаун, новый раунд) игрок
	// InvulnTime секунд неуязвим; InvulnTime ≤ 0 отключает неуязвимость.
	RespawnDelay         float64 // секунды
	RespawnMinKillerDist float64
	InvulnTime           float64 // секунды

	// Zombie — режим зомби (zombies.go): волны зомби вместо боя между игроками.
	// Zombie.WaveBase ≤ 0 выключает режим. Включается ZombieModeConfig.
	Zombie ZombieConfig

	// Seed инициализирует генератор мира (точки респавна). Одинаковый seed
	// и одинаковые вводы дают одинаковую симуляцию.
	Seed uint64
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig() Config {
	return Config{
		Map:         DefaultMap(),
		PlayerSpeed: 200, PlayerRadius: 12, HitRadius: 12,
		BulletSpeed: 500, BulletRadius: 3, BulletLifetime: 1.5, FireCooldown: 0.2, BulletDamage: 25, FireHoldTime: 0.1,
		HeatPerShot: 0.2, HeatDecay: 0.5, MaxSpread: 0.25,
		RoundTime: 120, KillLimit: 10, IntermissionTime: 8,
		PickupInterval: 20, MaxPickups: 3, PickupRadius: 10, PickupMinPlayerDist: 40,
		PickupMinPickupDist: 60, PickupEdgeMargin: 50,
		HealthAmount: 50, SpeedMultiplier: 1.6, SpeedDuration: 5,
		StartAmmo: 12, MaxAmmo: 30, AmmoAmount: 10, AmmoPickupInterval: 5, MaxAmmoPickups: 2, AmmoBasePlayers: 2,
		CrateHits: 5,
		DashSpeed: 700, DashDuration: 0.15, DashCooldown: 3,
		RespawnDelay: 3, RespawnMinKillerDist: 150, InvulnTime: 1,
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
	events     []Event    // события последнего тика
	ids        []PlayerID // переиспользуемый буфер отсортированных ID
	rng        *rand.Rand // единственный источник случайности мира

	phase    Phase
	timeLeft float64  // до конца текущей фазы
	winner   PlayerID // победитель завершённого раунда (в intermission)

	pickups     []Pickup
	nextPickup  PickupID
	pickupTimer float64 // секунд до следующего появления health/speed
	ammoTimer   float64 // секунд до следующего появления ammo

	crateHP []int // прочность препятствий параллельно cfg.Obstacles (crates.go)

	zg zombieGame // режим зомби (zombies.go); пусто, если он выключен
}

// NewWorld создаёт пустой мир; первый раунд начинается сразу. Случайность
// берётся только из генератора, засеянного cfg.Seed, поэтому симуляция воспроизводима.
func NewWorld(cfg Config) *World {
	w := &World{
		cfg:         cfg,
		players:     make(map[PlayerID]*Player),
		rng:         rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15)),
		phase:       PhasePlay,
		timeLeft:    max(cfg.RoundTime, 0),
		pickupTimer: cfg.PickupInterval,
		ammoTimer:   cfg.AmmoPickupInterval,
		crateHP:     newCrateHP(cfg),
	}
	if w.zombiesOn() {
		w.zg = newZombieGame(cfg)
	}
	return w
}

// Config возвращает параметры мира. Room берёт отсюда размеры для Welcome,
// чтобы клиент и сервер использовали один источник истины.
func (w *World) Config() Config { return w.cfg }

// Tick возвращает номер последнего выполненного тика.
func (w *World) Tick() uint64 { return w.tick }

// Phase возвращает текущую фазу раунда.
func (w *World) Phase() Phase { return w.phase }

// PlayerCount возвращает число игроков в мире.
func (w *World) PlayerCount() int { return len(w.players) }

// HasPlayer сообщает, есть ли игрок с данным ID.
func (w *World) HasPlayer(id PlayerID) bool {
	_, ok := w.players[id]
	return ok
}

// AddPlayer добавляет игрока в детерминированную точку спавна. Игрок входит
// в текущий раунд (в любой фазе) с нулевым счётом и неуязвимостью.
// Повторное добавление существующего ID ничего не делает.
func (w *World) AddPlayer(id PlayerID) {
	if _, ok := w.players[id]; ok {
		return
	}
	w.players[id] = &Player{
		ID: id, Pos: w.spawnPoint(id), HP: StartHP, Score: 0, Ammo: w.cfg.StartAmmo,
		invulnLeft: max(w.cfg.InvulnTime, 0),
	}
}

// RemovePlayer удаляет игрока. Идемпотентно.
func (w *World) RemovePlayer(id PlayerID) {
	delete(w.players, id)
}

// Update продвигает симуляцию на dt секунд, применяя inputs.
// Вводы для неизвестных и мёртвых игроков и устаревшие (seq <= LastSeq)
// игнорируются. Игрок без нового ввода продолжает двигаться по последнему направлению.
//
// В intermission мир заморожен: вводы отбрасываются целиком (LastSeq не растёт),
// игроки и пули стоят, идёт только таймер паузы; по его истечении — новый раунд.
func (w *World) Update(dt float64, inputs map[PlayerID]Input) {
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return
	}
	w.events = w.events[:0] // события предыдущего тика уже ушли в его снапшот
	if w.phase == PhaseIntermission || w.phase == PhaseGameOver {
		w.timeLeft = max(w.timeLeft-dt, 0)
		if w.timeLeft <= timeEps {
			w.startRound()
		}
		w.tick++
		return
	}

	// Порядок по ID — чтобы результат не зависел от порядка обхода map.
	ids := w.sortedIDs()
	w.updateRespawns(dt, ids) // до вводов: возродившийся в этом тике уже управляем

	for id, in := range inputs {
		p, ok := w.players[id]
		if !ok || p.dead() || in.Seq <= p.LastSeq {
			continue
		}
		p.Move = sanitizeMove(in.MoveX, in.MoveY)
		if a, ok := sanitizeAngle(in.Angle); ok {
			p.Angle = a
		}
		p.Fire = in.Fire
		p.fireLeft = w.cfg.FireHoldTime
		p.dashReq = in.Dash
		p.LastSeq = in.Seq
	}

	for _, p := range w.players {
		if p.dead() {
			continue
		}
		p.Pos = w.move(p.Pos, w.displacement(p, dt))
	}

	w.updatePickups(dt, ids)
	w.updateBullets(dt, ids)
	w.fire(dt, ids)
	if w.zombiesOn() {
		w.updateZombies(dt, ids) // после пуль: убитые в этом тике уже не кусают
	}
	// Неуязвимость тикает после попаданий: выданная в тике T защищает от
	// попаданий ровно InvulnTime — в тиках T … T+InvulnTime/dt−1.
	// Fire без свежего ввода гаснет после выстрелов: ввод тика T с FireHoldTime
	// 0.1 действует в тиках T и T+1 — меньше FireCooldown, повторного выстрела нет.
	for _, id := range ids {
		p := w.players[id]
		p.invulnLeft = max(p.invulnLeft-dt, 0)
		if w.cfg.FireHoldTime > 0 && p.Fire {
			if p.fireLeft -= dt; p.fireLeft <= timeEps {
				p.Fire = false
			}
		}
	}
	w.advanceRound(dt, ids)
	if w.zombiesOn() {
		w.checkGameOver(ids)
	}
	w.tick++
}

// updateRespawns ведёт таймеры мёртвых игроков и возрождает тех, у кого он
// истёк. Убитый в тике T возрождается в тике T+RespawnDelay/dt.
func (w *World) updateRespawns(dt float64, ids []PlayerID) {
	for _, id := range ids { // порядок по ID — детерминированный rng
		p := w.players[id]
		if p.respawnLeft <= 0 {
			continue // жив или ждёт волны (waitWave): таймера нет
		}
		p.respawnLeft -= dt
		if p.respawnLeft <= timeEps {
			w.respawn(p)
		}
	}
}

// killPlayer — HP цели упал до нуля: стрелок получает очко, цель умирает на
// RespawnDelay (или сразу возрождается, если задержка отключена).
func (w *World) killPlayer(target *Player, shooter PlayerID) {
	if s, ok := w.players[shooter]; ok {
		s.Score++
	}
	w.emit(Event{Type: EventKill, Target: target.ID, Shooter: shooter})
	target.killer = shooter
	if w.cfg.RespawnDelay <= 0 {
		w.respawn(target)
		return
	}
	target.HP = 0
	target.respawnLeft = w.cfg.RespawnDelay
	target.Move, target.Fire = Vec2{}, false
	target.speedLeft = 0
	target.dashReq, target.dashLeft = false, 0
}

// respawn возрождает игрока в случайной свободной точке подальше от убийцы
// с полным HP, стартовыми патронами и неуязвимостью.
func (w *World) respawn(p *Player) {
	p.respawnLeft = 0
	p.HP = StartHP
	p.Ammo = w.cfg.StartAmmo
	p.Pos = w.respawnPoint(p.ID, p.killer)
	p.killer = 0
	p.cooldown, p.Heat = 0, 0
	p.dashCd = 0
	p.invulnLeft = max(w.cfg.InvulnTime, 0)
	w.emit(Event{Type: EventRespawn, Player: p.ID, Pos: p.Pos})
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

// startRound начинает новый раунд: у всех полное HP и нулевой счёт, мёртвые
// оживают, все респавнятся в случайных свободных точках с неуязвимостью, пули,
// пикапы и вооружение сбрасываются, сломанные ящики восстанавливаются.
func (w *World) startRound() {
	w.phase = PhasePlay
	w.timeLeft = max(w.cfg.RoundTime, 0)
	w.winner = 0
	clear(w.bullets)
	w.bullets = w.bullets[:0]
	w.resetPickups()
	w.resetCrates()
	for _, id := range w.sortedIDs() { // порядок по ID — детерминированный rng
		p := w.players[id]
		p.HP = StartHP
		p.Score = 0
		p.Ammo = w.cfg.StartAmmo
		p.respawnLeft, p.killer, p.waitWave = 0, 0, false
		p.invulnLeft = max(w.cfg.InvulnTime, 0)
		p.Pos = w.respawnPoint(id, 0)
		p.Move = Vec2{}
		p.Fire = false
		p.cooldown = 0
		p.Heat = 0
		p.speedLeft = 0
		p.dashReq, p.dashLeft, p.dashCd = false, 0, 0
	}
	if w.zombiesOn() {
		w.resetZombieGame() // после игроков: отсчёт первой волны, если они есть
	}
}

// contactEps допускает погрешность float при сравнении с точкой касания.
const contactEps = 1e-9

// move сдвигает игрока на d с учётом краёв мира и препятствий. Оси разрешаются
// раздельно — сначала X, затем Y (уже с новым X): упёршись в стену по одной оси,
// игрок продолжает скользить вдоль неё по другой.
func (w *World) move(pos, d Vec2) Vec2 { return w.moveCircle(pos, d, w.cfg.PlayerRadius) }

// moveCircle — то же для круга радиуса r (зомби).
func (w *World) moveCircle(pos, d Vec2, r float64) Vec2 {
	lo, hi := w.boundsFor(r)
	if d.X != 0 {
		x := clamp(pos.X+d.X, lo.X, hi.X)
		for i, o := range w.cfg.Obstacles {
			if w.broken(i) {
				continue
			}
			x = sweepAxis(pos.X, x, pos.Y, o.X, o.X+o.W, o.Y, o.Y+o.H, r)
		}
		pos.X = x
	}
	if d.Y != 0 {
		y := clamp(pos.Y+d.Y, lo.Y, hi.Y)
		for i, o := range w.cfg.Obstacles {
			if w.broken(i) {
				continue
			}
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
	for i, o := range w.cfg.Obstacles {
		if w.broken(i) {
			continue
		}
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
	t, _, ok := w.firstObstacle(from, to)
	return t, ok
}

// firstObstacle — то же плюс индекс препятствия в cfg.Obstacles. Сломанные
// ящики пропускаются.
func (w *World) firstObstacle(from, to Vec2) (float64, int, bool) {
	best, idx, found := math.Inf(1), -1, false
	for i, o := range w.cfg.Obstacles {
		if w.broken(i) {
			continue
		}
		if t, ok := segmentRectT(from, to, o, w.cfg.BulletRadius); ok && t < best {
			best, idx, found = t, i, true
		}
	}
	return best, idx, found
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

		var (
			target *Player
			at     Vec2
			tHit   float64
			hit    bool
			zi     = -1 // индекс зомби, если пуля попала в него раньше, чем в игрока
		)
		if w.zombiesOn() {
			// В режиме зомби игроки не ранят друг друга: пули летят сквозь них.
			zi, at, tHit, hit = w.firstZombie(from, to)
		} else {
			target, at, tHit, hit = w.firstHit(b.Owner, from, to, ids)
		}
		if tWall, wi, wall := w.firstObstacle(from, to); wall && (!hit || tWall < tHit) {
			// Пуля разбилась о препятствие; Pos — центр пули в момент касания.
			// О целый ящик — минус прочность (crateHit/crateBroken), иначе bulletHit.
			at := Vec2{X: from.X + (to.X-from.X)*tWall, Y: from.Y + (to.Y-from.Y)*tWall}
			if w.destructible(wi) {
				w.damageCrate(wi, at)
			} else {
				w.emit(Event{Type: EventBulletHit, Pos: at})
			}
			continue
		}
		if hit {
			if zi >= 0 {
				w.hitZombie(zi, b.Owner, at)
			} else {
				w.applyHit(b.Owner, target, at)
			}
			continue
		}
		if to.X < 0 || to.X > w.cfg.Width || to.Y < 0 || to.Y > w.cfg.Height {
			w.emit(Event{Type: EventBulletHit, Pos: w.exitPoint(from, to)}) // ударилась о границу
			continue
		}
		if b.ttl <= timeEps {
			continue // истекла в полёте — без события
		}
		b.Pos = to
		alive = append(alive, b)
	}
	clear(w.bullets[len(alive):]) // не держим ссылок на хвост
	w.bullets = alive
}

// firstHit возвращает ближайшего к началу отрезка игрока (кроме владельца
// и мёртвых — сквозь них пуля проходит), которого задевает пуля на пути
// from→to, точку попадания и её параметр t∈[0, 1].
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
		if p.dead() {
			continue
		}
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

// applyHit наносит урон; при HP ≤ 0 цель погибает (см. killPlayer).
// Неуязвимая цель останавливает пулю без урона — как стена, событием bulletHit.
func (w *World) applyHit(shooter PlayerID, target *Player, at Vec2) {
	if target.invulnerable() {
		w.emit(Event{Type: EventBulletHit, Pos: at})
		return
	}
	target.HP -= w.cfg.BulletDamage
	w.emit(Event{Type: EventHit, Pos: at, Target: target.ID, Shooter: shooter})
	if target.HP <= 0 {
		w.killPlayer(target, shooter)
	}
}

// fire создаёт пули для игроков с зажатым огнём и истёкшим кулдауном.
// Пуля вылетает из позиции игрока; по владельцу она не попадает (см. firstHit).
//
// Разброс берётся от heat до выстрела: первый выстрел с холодного ствола точен.
// В тиках без выстрела (огонь отпущен, кулдаун или нет патронов) heat остывает.
// Игроки обходятся по ID, поэтому последовательность rng детерминирована.
func (w *World) fire(dt float64, ids []PlayerID) {
	for _, id := range ids {
		p := w.players[id]
		if p.dead() {
			continue
		}
		p.cooldown = math.Max(0, p.cooldown-dt)
		if !p.Fire || p.cooldown > timeEps || !w.hasAmmo(p) {
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
		w.emit(Event{Type: EventShot, Player: id, Pos: p.Pos})
		p.cooldown = w.cfg.FireCooldown
		if w.cfg.MaxAmmo > 0 {
			p.Ammo--
		}
	}
}

// hasAmmo — может ли игрок выстрелить по патронам. При MaxAmmo ≤ 0 патроны
// отключены и стрельба не ограничена.
func (w *World) hasAmmo(p *Player) bool {
	return w.cfg.MaxAmmo <= 0 || p.Ammo > 0
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
		respawnIn := 0.0
		switch {
		case p.respawnLeft > 0:
			respawnIn = p.respawnLeft
		case p.waitWave && !w.zg.inWave:
			respawnIn = w.zg.nextWave // воскреснет с началом следующей волны
		}
		ps = append(ps, PlayerState{
			ID:      p.ID,
			Pos:     p.Pos,
			Angle:   p.Angle,
			Heat:    p.Heat,
			HP:      p.HP,
			Score:   p.Score,
			Ammo:    p.Ammo,
			Boosted: p.speedLeft > timeEps,
			DashCd:  p.dashCooldownLeft(),
			LastSeq: p.LastSeq,

			Dead:      p.dead(),
			RespawnIn: respawnIn,
			Invuln:    p.invulnerable(),
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
	var zombies []ZombieState
	if w.zombiesOn() {
		zombies = w.zombieStates()
		round.Zombies = true
		round.Wave, round.ZombiesLeft, round.Kills = w.zg.wave, len(zombies), w.zg.kills
		if w.phase == PhasePlay {
			round.TimeLeft = 0
			if !w.zg.inWave && w.zg.started {
				round.TimeLeft = w.zg.nextWave
			}
		}
	}
	return Snapshot{
		Zombies: zombies,
		Tick:    w.tick,
		Players: ps,
		Bullets: bs,
		Events:  slices.Clone(w.events),
		Pickups: slices.Clone(w.pickups), // ID растут при добавлении, порядок сохраняется
		Crates:  w.crateStates(),
		Round:   round,
	}
}

// spawnSlots — число фиксированных точек спавна на окружности (для карты без Spawns).
const spawnSlots = 8

// spawnPoint — точка входа игрока. На карте со спавнами — спавн номер id
// по модулю их числа, а если там уже стоит живой игрок — следующий свободный.
// Без спавнов — окружность вокруг центра арены.
func (w *World) spawnPoint(id PlayerID) Vec2 {
	n := len(w.cfg.Spawns)
	if n == 0 {
		return w.spawnSlot(uint32(id))
	}
	for i := range n {
		p := w.cfg.Spawns[(int(id)+i)%n]
		if !w.spawnOccupied(p, id) {
			return p
		}
	}
	return w.cfg.Spawns[int(id)%n]
}

// spawnOccupied — стоит ли у точки p живой игрок (кроме self): тела бы перекрылись.
func (w *World) spawnOccupied(p Vec2, self PlayerID) bool {
	d := 2 * w.cfg.PlayerRadius
	for id, q := range w.players {
		if id == self || q.dead() {
			continue
		}
		if dx, dy := q.Pos.X-p.X, q.Pos.Y-p.Y; dx*dx+dy*dy < d*d {
			return true
		}
	}
	return false
}

// spawnSlot — точка спавна номер slot (по модулю spawnSlots).
func (w *World) spawnSlot(slot uint32) Vec2 {
	angle := 2 * math.Pi * float64(slot%spawnSlots) / spawnSlots
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

// respawnPoint возвращает случайную свободную от препятствий точку не ближе
// RespawnMinKillerDist к убийце killer (0, сам игрок или ушедший из мира —
// без ограничения). Случайность — только из генератора мира, не глобальный rand.
func (w *World) respawnPoint(id, killer PlayerID) Vec2 {
	var (
		avoid Vec2
		minD2 float64 // 0 — ограничения нет
	)
	if k, ok := w.players[killer]; ok && killer != id && w.cfg.RespawnMinKillerDist > 0 {
		avoid, minD2 = k.Pos, w.cfg.RespawnMinKillerDist*w.cfg.RespawnMinKillerDist
	}
	farEnough := func(p Vec2) bool {
		dx, dy := p.X-avoid.X, p.Y-avoid.Y
		return dx*dx+dy*dy >= minD2
	}
	if len(w.cfg.Spawns) > 0 {
		return w.mapRespawnPoint(id, avoid, farEnough)
	}

	lo, hi := w.bounds()
	for range respawnAttempts {
		p := Vec2{
			X: lo.X + w.rng.Float64()*(hi.X-lo.X),
			Y: lo.Y + w.rng.Float64()*(hi.Y-lo.Y),
		}
		if w.isFree(p) && farEnough(p) {
			return p
		}
	}
	// Фиксированные точки свободны по построению карты (см. DefaultObstacles);
	// начиная со своей, берём первую достаточно далёкую от убийцы.
	for i := range uint32(spawnSlots) {
		if p := w.spawnSlot(uint32(id) + i); farEnough(p) {
			return p
		}
	}
	return w.spawnPoint(id)
}

// mapRespawnPoint — респаун на карте со спавнами: случайный спавн, достаточно
// далёкий от убийцы и не занятый живым игроком. Если таких нет — самый далёкий
// от убийцы. Кандидаты перебираются в порядке среза, rng — один бросок: симуляция
// воспроизводима.
func (w *World) mapRespawnPoint(id PlayerID, avoid Vec2, farEnough func(Vec2) bool) Vec2 {
	var cand [16]int // спавнов на карте немного; при большем числе append вырастит срез
	ok := cand[:0]
	for i, p := range w.cfg.Spawns {
		if farEnough(p) && !w.spawnOccupied(p, id) {
			ok = append(ok, i)
		}
	}
	if len(ok) > 0 {
		return w.cfg.Spawns[ok[w.rng.IntN(len(ok))]]
	}
	best, bestD2 := w.cfg.Spawns[0], -1.0
	for _, p := range w.cfg.Spawns {
		if dx, dy := p.X-avoid.X, p.Y-avoid.Y; dx*dx+dy*dy > bestD2 {
			best, bestD2 = p, dx*dx+dy*dy
		}
	}
	return best
}

// bounds — допустимый диапазон центра игрока: [0, Width]×[0, Height] с отступом
// на радиус, чтобы тело не вылезало за край. Если радиус больше половины мира,
// отступ урезается — иначе нижняя граница оказалась бы за верхней.
func (w *World) bounds() (lo, hi Vec2) { return w.boundsFor(w.cfg.PlayerRadius) }

// boundsFor — то же для круга радиуса r.
func (w *World) boundsFor(r float64) (lo, hi Vec2) {
	rx := math.Min(r, w.cfg.Width/2)
	ry := math.Min(r, w.cfg.Height/2)
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
