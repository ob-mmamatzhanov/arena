package game

import "math"

// Режим зомби: игроки вместе отбиваются от волн зомби и не ранят друг друга.
//
//   - Первая волна — через FirstWaveDelay после появления первого игрока.
//     Волна n: WaveBase + WaveStep·(n−1) зомби со скоростью Speed + SpeedStep·(n−1).
//   - Зомби появляются у краёв мира: не в препятствиях, в достижимой клетке
//     и не ближе SpawnMinPlayerDist к живым игрокам (если такой точки не нашлось —
//     самая далёкая из найденных).
//   - Идут по flow field: сетка FlowCell, BFS от клеток живых игроков с учётом
//     препятствий, пересчёт раз в FlowEvery тиков. В соседней с игроком клетке
//     (и если клетка недостижима) — прямо к ближайшему живому игроку.
//   - Касание (центры ближе PlayerRadius + Radius) — ContactDamage урона не
//     чаще раза в ContactCooldown на зомби; неуязвимых не кусают.
//   - Волна кончается, когда убиты все её зомби; следующая — через WavePause.
//     Убитые зомби игроки мертвы до начала следующей волны.
//   - Все игроки мертвы — PhaseGameOver с номером волны и числом убитых зомби;
//     через IntermissionTime игра начинается заново (startRound).
//   - Игроков не осталось — игра сбрасывается до входа следующего.
//
// Зомби не сталкиваются друг с другом и с игроками: тела могут перекрываться.

// ZombieID — идентификатор зомби, уникальный в пределах мира.
type ZombieID uint32

// Zombie — зомби на карте.
type Zombie struct {
	ID       ZombieID
	Pos      Vec2
	HP       int
	Speed    float64 // единиц в секунду
	attackCd float64 // секунд до следующего укуса
}

// ZombieState — копия зомби для снапшота.
type ZombieState struct {
	ID    ZombieID
	Pos   Vec2
	HP    int
	Speed float64
}

// ZombieConfig — параметры режима зомби. WaveBase ≤ 0 выключает режим.
type ZombieConfig struct {
	WaveBase  int     // зомби в первой волне
	WaveStep  int     // прибавка зомби за волну
	Speed     float64 // скорость в первой волне, единиц в секунду
	SpeedStep float64 // прибавка скорости за волну
	HP        int
	Radius    float64 // радиус тела и хитбокса

	FirstWaveDelay     float64 // секунд от входа первого игрока до первой волны
	WavePause          float64 // секунд между волнами
	SpawnMinPlayerDist float64

	FlowCell  float64 // сторона клетки flow field
	FlowEvery int     // пересчёт flow field раз в столько тиков

	ContactDamage   int
	ContactCooldown float64 // секунд между укусами одного зомби
}

// DefaultZombieConfig — параметры режима зомби для продакшена.
func DefaultZombieConfig() ZombieConfig {
	return ZombieConfig{
		WaveBase: 4, WaveStep: 2, Speed: 90, SpeedStep: 5, HP: 30, Radius: 12,
		FirstWaveDelay: 5, WavePause: 10, SpawnMinPlayerDist: 200,
		FlowCell: 20, FlowEvery: 5,
		ContactDamage: 10, ContactCooldown: 0.5,
	}
}

// ZombieModeConfig включает в cfg режим зомби: волны по DefaultZombieConfig,
// без лимитов раунда по времени и фрагам (игра идёт до gameover).
func ZombieModeConfig(cfg Config) Config {
	cfg.Zombie = DefaultZombieConfig()
	cfg.RoundTime, cfg.KillLimit = 0, 0
	return cfg
}

func (w *World) zombiesOn() bool { return w.cfg.Zombie.WaveBase > 0 }

// zombieGame — состояние режима зомби.
type zombieGame struct {
	zombies []Zombie // по возрастанию ID; убитые пулями удаляются в updateZombies
	nextID  ZombieID
	wave    int
	kills   int

	started  bool    // игра идёт: отсчёт до первой волны запущен
	inWave   bool    // волна идёт (её зомби ещё живы)
	nextWave float64 // секунд до следующей волны, пока !inWave

	flow     flowField
	flowWait int // тиков до пересчёта flow field
}

// zombiesCap — начальная ёмкость среза зомби: волны до ~30 зомби не
// аллоцируют в Update.
const zombiesCap = 32

func newZombieGame(cfg Config) zombieGame {
	return zombieGame{zombies: make([]Zombie, 0, zombiesCap), flow: newFlowField(cfg)}
}

// resetZombieGame начинает игру заново: без зомби, с волны 0. Если игроки
// есть, сразу идёт отсчёт первой волны; иначе он начнётся со входом первого.
func (w *World) resetZombieGame() {
	zg := &w.zg
	zg.zombies = zg.zombies[:0]
	zg.wave, zg.kills = 0, 0
	zg.inWave = false
	zg.started = len(w.players) > 0
	zg.nextWave = w.cfg.Zombie.FirstWaveDelay
}

// updateZombies — тик режима зомби в фазе play: волны, движение, укусы.
func (w *World) updateZombies(dt float64, ids []PlayerID) {
	zg := &w.zg
	// Убитые пулями этого тика: фильтр на месте, порядок ID сохраняется.
	alive := zg.zombies[:0]
	for _, z := range zg.zombies {
		if z.HP > 0 {
			alive = append(alive, z)
		}
	}
	zg.zombies = alive

	if len(ids) == 0 {
		if zg.started || zg.wave > 0 {
			w.resetZombieGame()
		}
		return
	}
	// Отсчёт, запущенный в тике T, в нём не тикает: следующая волна — ровно
	// в тике T + задержка/dt (как респаун в updateRespawns).
	if !zg.started {
		w.resetZombieGame() // вошёл первый игрок: отсчёт первой волны
		return
	}
	if zg.inWave && len(zg.zombies) == 0 {
		zg.inWave = false
		zg.nextWave = w.cfg.Zombie.WavePause
		return
	}
	if !zg.inWave {
		zg.nextWave = max(zg.nextWave-dt, 0)
		if zg.nextWave > timeEps {
			return
		}
		w.startWave(ids)
	}

	if zg.flowWait--; zg.flowWait <= 0 {
		w.rebuildFlow(ids)
	}
	zc := &w.cfg.Zombie
	reach := w.cfg.PlayerRadius + zc.Radius + contactEps
	for i := range zg.zombies {
		z := &zg.zombies[i]
		dir := w.zombieDir(z.Pos, ids)
		z.Pos = w.moveCircle(z.Pos, Vec2{X: dir.X * z.Speed * dt, Y: dir.Y * z.Speed * dt}, zc.Radius)

		z.attackCd = max(z.attackCd-dt, 0)
		if z.attackCd > timeEps {
			continue
		}
		for _, id := range ids { // по ID: при нескольких целях кусает младшую
			p := w.players[id]
			if p.dead() || p.invulnerable() {
				continue
			}
			if dx, dy := p.Pos.X-z.Pos.X, p.Pos.Y-z.Pos.Y; dx*dx+dy*dy <= reach*reach {
				w.bitePlayer(p)
				z.attackCd = zc.ContactCooldown
				break
			}
		}
	}
}

// startWave воскрешает ждущих волны игроков и выпускает зомби следующей волны.
func (w *World) startWave(ids []PlayerID) {
	zg, zc := &w.zg, &w.cfg.Zombie
	zg.wave++
	zg.inWave = true
	for _, id := range ids { // до спавна зомби: те появятся подальше и от воскресших
		if p := w.players[id]; p.waitWave {
			p.waitWave = false
			w.respawn(p)
		}
	}
	w.zg.flow.dirty = true // на всякий случай: проходимость считается заново раз в волну
	w.rebuildFlow(ids)     // спавн проверяет достижимость по свежему полю
	n := zc.WaveBase + zc.WaveStep*(zg.wave-1)
	speed := zc.Speed + zc.SpeedStep*float64(zg.wave-1)
	for range n {
		zg.nextID++
		zg.zombies = append(zg.zombies, Zombie{ID: zg.nextID, Pos: w.zombieSpawnPoint(ids), HP: zc.HP, Speed: speed})
	}
}

// bitePlayer — укус зомби: урон, при HP ≤ 0 игрок мёртв до следующей волны.
// События hit и kill — с Shooter = 0.
func (w *World) bitePlayer(p *Player) {
	p.HP -= w.cfg.Zombie.ContactDamage
	w.emit(Event{Type: EventHit, Pos: p.Pos, Target: p.ID})
	if p.HP > 0 {
		return
	}
	w.emit(Event{Type: EventKill, Target: p.ID})
	p.HP = 0
	p.waitWave = true
	p.killer = 0
	p.Move, p.Fire = Vec2{}, false
	p.speedLeft = 0
	p.dashReq, p.dashLeft = false, 0
}

// checkGameOver — все игроки мертвы: игра окончена.
func (w *World) checkGameOver(ids []PlayerID) {
	if w.phase != PhasePlay || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if !w.players[id].dead() {
			return
		}
	}
	w.phase = PhaseGameOver
	w.timeLeft = max(w.cfg.IntermissionTime, 0)
	if w.timeLeft <= timeEps {
		w.startRound()
	}
}

// firstZombie — ближайший к началу отрезка живой зомби на пути пули from→to:
// индекс, точка попадания и её параметр t∈[0, 1].
func (w *World) firstZombie(from, to Vec2) (int, Vec2, float64, bool) {
	r := w.cfg.Zombie.Radius + w.cfg.BulletRadius
	d := Vec2{X: to.X - from.X, Y: to.Y - from.Y}
	dd := d.X*d.X + d.Y*d.Y
	best, bestT, bestP := -1, math.Inf(1), Vec2{}
	for i := range w.zg.zombies {
		z := &w.zg.zombies[i]
		if z.HP <= 0 {
			continue // убит другой пулей в этом тике
		}
		t := 0.0
		if dd > 0 {
			t = clamp(((z.Pos.X-from.X)*d.X+(z.Pos.Y-from.Y)*d.Y)/dd, 0, 1)
		}
		c := Vec2{X: from.X + d.X*t, Y: from.Y + d.Y*t}
		if dx, dy := c.X-z.Pos.X, c.Y-z.Pos.Y; dx*dx+dy*dy <= r*r && t < bestT {
			best, bestT, bestP = i, t, c
		}
	}
	return best, bestP, bestT, best >= 0
}

// hitZombie — пуля стрелка shooter попала в зомби i в точке at. Ранение —
// bulletHit (пуля погасла), убийство — zombieKill и +1 к счёту стрелка.
// Убитый остаётся в срезе с HP ≤ 0 до updateZombies этого же тика.
func (w *World) hitZombie(i int, shooter PlayerID, at Vec2) {
	z := &w.zg.zombies[i]
	z.HP -= w.cfg.BulletDamage
	if z.HP > 0 {
		w.emit(Event{Type: EventBulletHit, Pos: at})
		return
	}
	w.zg.kills++
	if s, ok := w.players[shooter]; ok {
		s.Score++
	}
	w.emit(Event{Type: EventZombieKill, Pos: z.Pos, Zombie: z.ID, Shooter: shooter})
}

// zombieStates — копия живых зомби для снапшота; nil, если их нет.
func (w *World) zombieStates() []ZombieState {
	var out []ZombieState
	for _, z := range w.zg.zombies {
		if z.HP > 0 {
			out = append(out, ZombieState{ID: z.ID, Pos: z.Pos, HP: z.HP, Speed: z.Speed})
		}
	}
	return out
}

// zombieSpawnAttempts — сколько случайных точек у края пробовать.
const zombieSpawnAttempts = 64

// zombieSpawnPoint — точка у случайного края мира: свободная от препятствий,
// достижимая по flow field и не ближе SpawnMinPlayerDist к живым игрокам.
// Если такой не нашлось — самая далёкая от игроков из свободных достижимых.
func (w *World) zombieSpawnPoint(ids []PlayerID) Vec2 {
	r := w.cfg.Zombie.Radius
	minD2 := w.cfg.Zombie.SpawnMinPlayerDist * w.cfg.Zombie.SpawnMinPlayerDist
	best, bestD2 := Vec2{}, -1.0
	for range zombieSpawnAttempts {
		p := w.edgePoint(r)
		if !w.circleFree(p, r) || !w.zg.flow.reachable(p) {
			continue
		}
		_, d2 := w.nearestAlive(p, ids)
		if d2 >= minD2 {
			return p
		}
		if d2 > bestD2 {
			best, bestD2 = p, d2
		}
	}
	if bestD2 >= 0 {
		return best
	}
	return w.edgePoint(r) // карта у краёв сплошь занята — так не бывает на наших картах
}

// edgePoint — случайная точка на расстоянии r от случайного края мира.
func (w *World) edgePoint(r float64) Vec2 {
	lo, hi := w.boundsFor(r)
	t := w.rng.Float64()
	switch w.rng.IntN(4) {
	case 0:
		return Vec2{X: lo.X + t*(hi.X-lo.X), Y: lo.Y}
	case 1:
		return Vec2{X: lo.X + t*(hi.X-lo.X), Y: hi.Y}
	case 2:
		return Vec2{X: lo.X, Y: lo.Y + t*(hi.Y-lo.Y)}
	default:
		return Vec2{X: hi.X, Y: lo.Y + t*(hi.Y-lo.Y)}
	}
}

// nearestAlive — ближайший к p живой игрок из ids (nil, если живых нет) и
// квадрат расстояния до него. Обход по ids — детерминированный выбор при равенстве.
func (w *World) nearestAlive(p Vec2, ids []PlayerID) (*Player, float64) {
	var best *Player
	bestD2 := math.Inf(1)
	for _, id := range ids {
		q := w.players[id]
		if q.dead() {
			continue
		}
		if dx, dy := q.Pos.X-p.X, q.Pos.Y-p.Y; dx*dx+dy*dy < bestD2 {
			best, bestD2 = q, dx*dx+dy*dy
		}
	}
	return best, bestD2
}

// zombieDir — единичное направление движения зомби из pos (0 — стоять).
func (w *World) zombieDir(pos Vec2, ids []PlayerID) Vec2 {
	target, _ := w.nearestAlive(pos, ids)
	if target == nil {
		return Vec2{}
	}
	f := &w.zg.flow
	c := f.cellOf(pos)
	goal := target.Pos // рядом с игроком (и из тупика) — напрямую к нему
	// Иначе — шаг в соседнюю клетку, ближайшую к игрокам. Из недостижимой
	// клетки (центр её в препятствии) — в любую достижимую соседнюю.
	if d := f.dist[c]; d < 0 || d > 1 {
		if next := f.downhill(c); next >= 0 {
			goal = f.anchor[next]
		}
	}
	dir := unit(Vec2{X: goal.X - pos.X, Y: goal.Y - pos.Y})
	push := w.obstaclePush(pos, w.cfg.Zombie.Radius)
	return unit(Vec2{X: dir.X + push.X, Y: dir.Y + push.Y})
}

// pushRange — на сколько дальше своего радиуса тело чувствует препятствия.
const pushRange = 6

// obstaclePush — отталкивание круга радиуса r от препятствий ближе r+pushRange:
// сумма нормалей от ближайших точек, сила растёт к касанию. Кратчайший путь
// по клеткам прижимается к стенам (центры клеток в 10 px от них), и без
// отталкивания тело радиусом 12 цепляется за угол; в узком проходе силы от
// двух стен гасят друг друга.
func (w *World) obstaclePush(p Vec2, r float64) Vec2 {
	var push Vec2
	reach := r + pushRange
	for i, o := range w.cfg.Obstacles {
		if w.broken(i) {
			continue
		}
		dx := p.X - clamp(p.X, o.X, o.X+o.W)
		dy := p.Y - clamp(p.Y, o.Y, o.Y+o.H)
		d2 := dx*dx + dy*dy
		if d2 >= reach*reach || d2 == 0 {
			continue
		}
		d := math.Sqrt(d2)
		k := (reach - d) / (pushRange * d) // 1 в касании (d = r), 0 на границе
		push.X += dx * k
		push.Y += dy * k
	}
	return push
}

// unit нормирует v до длины 1 (нулевой вектор остаётся нулевым).
func unit(v Vec2) Vec2 {
	l2 := v.X*v.X + v.Y*v.Y
	if l2 <= 0 {
		return Vec2{}
	}
	l := math.Sqrt(l2)
	return Vec2{X: v.X / l, Y: v.Y / l}
}

// flowField — поле расстояний до живых игроков на сетке клеток cell×cell.
// dist — шаги BFS (8 соседей, по диагонали — только если обе смежные клетки
// проходимы), −1 — клетка непроходима или недостижима.
//
// Клетка проходима, если тело зомби целиком помещается хотя бы в одной из
// flowSamples² точек внутри неё (шаг cell/flowSamples). Проверять один центр
// нельзя: при сетке 20 порог расстояния либо пропустит щель 16 px между
// ящиками (пуля пролетает, тело 24 — нет), либо может закрыть проход 32 px —
// смотря как ляжет сетка. Полоса допустимых центров в проходе 32 шириной 8,
// и при шаге 5 в неё всегда попадает точка, а в щели 16 таких центров нет.
// anchor — найденная точка, ближайшая к центру клетки: к ней и идёт зомби.
//
// Проходимость считается только при смене препятствий (dirty: начало волны,
// поломка и восстановление ящиков); BFS — каждые FlowEvery тиков. Буферы
// выделяются один раз.
type flowField struct {
	cell       float64
	cols, rows int
	blocked    []bool
	anchor     []Vec2
	dist       []int32
	queue      []int32
	dirty      bool
}

const flowSamples = 4

var flowNeighbors = [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}}

// flowSampleOrder — индексы точек клетки (i, j ∈ 0…flowSamples−1) от центра к краям.
var flowSampleOrder = func() (order [flowSamples * flowSamples][2]int) {
	k := 0
	for i := range flowSamples {
		for j := range flowSamples {
			order[k] = [2]int{i, j}
			k++
		}
	}
	mid := float64(flowSamples-1) / 2
	off := func(a [2]int) float64 {
		dx, dy := float64(a[0])-mid, float64(a[1])-mid
		return dx*dx + dy*dy
	}
	for a := 1; a < len(order); a++ { // вставками: стабильно и без пакетов
		for b := a; b > 0 && off(order[b]) < off(order[b-1]); b-- {
			order[b], order[b-1] = order[b-1], order[b]
		}
	}
	return order
}()

func newFlowField(cfg Config) flowField {
	cell := cfg.Zombie.FlowCell
	if cell <= 0 {
		cell = DefaultZombieConfig().FlowCell
	}
	cols := max(int(math.Ceil(cfg.Width/cell)), 1)
	rows := max(int(math.Ceil(cfg.Height/cell)), 1)
	n := cols * rows
	return flowField{
		cell: cell, cols: cols, rows: rows,
		blocked: make([]bool, n), anchor: make([]Vec2, n),
		dist: make([]int32, n), queue: make([]int32, 0, n),
		dirty: true,
	}
}

func (f *flowField) cellOf(p Vec2) int {
	cx := min(max(int(p.X/f.cell), 0), f.cols-1)
	cy := min(max(int(p.Y/f.cell), 0), f.rows-1)
	return cy*f.cols + cx
}

func (f *flowField) reachable(p Vec2) bool { return f.dist[f.cellOf(p)] >= 0 }

// step — соседняя клетка c в направлении k или −1 (за краем, непроходима,
// диагональ через угол препятствия).
func (f *flowField) step(c, k int) int {
	cx, cy := c%f.cols, c/f.cols
	dx, dy := flowNeighbors[k][0], flowNeighbors[k][1]
	nx, ny := cx+dx, cy+dy
	if nx < 0 || ny < 0 || nx >= f.cols || ny >= f.rows {
		return -1
	}
	n := ny*f.cols + nx
	if f.blocked[n] {
		return -1
	}
	if dx != 0 && dy != 0 && (f.blocked[cy*f.cols+nx] || f.blocked[ny*f.cols+cx]) {
		return -1
	}
	return n
}

// downhill — соседняя клетка с наименьшим dist, меньшим, чем у c (из
// недостижимой c — любая достижимая); −1, если такой нет.
func (f *flowField) downhill(c int) int {
	best, bestD := -1, f.dist[c]
	if bestD < 0 {
		bestD = math.MaxInt32
	}
	for k := range flowNeighbors {
		if n := f.step(c, k); n >= 0 && f.dist[n] >= 0 && f.dist[n] < bestD {
			best, bestD = n, f.dist[n]
		}
	}
	return best
}

// updatePassable пересчитывает проходимость и опорные точки клеток.
func (w *World) updatePassable() {
	f := &w.zg.flow
	r := w.cfg.Zombie.Radius
	lo, hi := w.boundsFor(r)
	step := f.cell / flowSamples
	for i := range f.blocked {
		x0, y0 := float64(i%f.cols)*f.cell, float64(i/f.cols)*f.cell
		f.blocked[i] = true
		for _, s := range flowSampleOrder {
			p := Vec2{X: x0 + (float64(s[0])+0.5)*step, Y: y0 + (float64(s[1])+0.5)*step}
			if p.X < lo.X || p.X > hi.X || p.Y < lo.Y || p.Y > hi.Y || !w.circleFree(p, r) {
				continue
			}
			f.blocked[i], f.anchor[i] = false, p
			break
		}
	}
	f.dirty = false
}

// rebuildFlow пересчитывает flow field: при смене препятствий — проходимость,
// затем BFS от клеток живых игроков. Без аллокаций.
func (w *World) rebuildFlow(ids []PlayerID) {
	f := &w.zg.flow
	w.zg.flowWait = w.cfg.Zombie.FlowEvery
	if f.dirty {
		w.updatePassable()
	}
	for i := range f.dist {
		f.dist[i] = -1
	}
	q := f.queue[:0]
	for _, id := range ids {
		p := w.players[id]
		if p.dead() {
			continue
		}
		// Клетку игрока засеваем, даже если она непроходима по выборке: сам
		// игрок в ней стоит.
		if c := f.cellOf(p.Pos); f.dist[c] != 0 {
			f.dist[c] = 0
			q = append(q, int32(c))
		}
	}
	for h := 0; h < len(q); h++ {
		c := int(q[h])
		for k := range flowNeighbors {
			if n := f.step(c, k); n >= 0 && f.dist[n] < 0 {
				f.dist[n] = f.dist[c] + 1
				q = append(q, int32(n))
			}
		}
	}
	f.queue = q
}
