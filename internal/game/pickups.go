package game

// PickupType — вид пикапа.
type PickupType string

const (
	PickupHealth PickupType = "health" // +HealthAmount hp, не выше StartHP
	PickupSpeed  PickupType = "speed"  // ×SpeedMultiplier к скорости на SpeedDuration
	PickupAmmo   PickupType = "ammo"   // +AmmoAmount патронов, не выше MaxAmmo

	// Оружие (weapons.go): заменяет текущее, с AmmoOnPickup патронов.
	PickupShotgun PickupType = "w_shotgun"
	PickupSMG     PickupType = "w_smg"
	PickupRifle   PickupType = "w_rifle"
)

// weaponPickups — пикапы оружия в порядке броска rng при спавне: смена порядка
// меняет симуляцию.
var weaponPickups = [...]struct {
	typ    PickupType
	weapon WeaponID
}{
	{PickupShotgun, WeaponShotgun},
	{PickupSMG, WeaponSMG},
	{PickupRifle, WeaponRifle},
}

// weaponOf — оружие пикапа typ (ok = false, если это не оружие).
func weaponOf(typ PickupType) (WeaponID, bool) {
	for _, wp := range weaponPickups {
		if wp.typ == typ {
			return wp.weapon, true
		}
	}
	return 0, false
}

// pickupStream — поток спавна пикапа: у каждого свой таймер и лимит на карте.
type pickupStream uint8

const (
	streamGeneral pickupStream = iota // health, speed
	streamAmmo
	streamWeapon
)

func streamOf(typ PickupType) pickupStream {
	if typ == PickupAmmo {
		return streamAmmo
	}
	if _, ok := weaponOf(typ); ok {
		return streamWeapon
	}
	return streamGeneral
}

// pickupWeights — веса типов в общем потоке спавна (в процентах, сумма
// pickupWeightTotal). Ammo и оружие сюда не входят — у них свои потоки.
// Порядок важен: бросок rng раскладывается по интервалам подряд, смена порядка
// меняет симуляцию.
var pickupWeights = [...]struct {
	typ    PickupType
	weight int
}{
	{PickupHealth, 60},
	{PickupSpeed, 40},
}

const pickupWeightTotal = 100

// pickupTypeFor отображает бросок roll ∈ [0, pickupWeightTotal) в тип пикапа:
// [0, 60) — health, [60, 100) — speed.
func pickupTypeFor(roll int) PickupType {
	for _, pw := range pickupWeights {
		if roll < pw.weight {
			return pw.typ
		}
		roll -= pw.weight
	}
	return pickupWeights[len(pickupWeights)-1].typ
}

// randomPickupType — взвешенный выбор типа из генератора мира.
func (w *World) randomPickupType() PickupType {
	return pickupTypeFor(w.rng.IntN(pickupWeightTotal))
}

// PickupID — идентификатор пикапа, уникальный в пределах мира.
type PickupID uint32

// Pickup — лежащий на карте пикап.
type Pickup struct {
	ID   PickupID
	Type PickupType
	Pos  Vec2
}

// pickupAttempts — сколько случайных точек пробуем за тик. Если все заняты
// (игроки рядом или препятствия), спавн повторяется на следующем тике.
const pickupAttempts = 32

// updatePickups: ускорение тикает, игроки подбирают пикапы, по таймеру
// появляется новый. Вызывается после движения, поэтому подбор учитывает
// позицию игрока в этом тике. Таймер ускорения уменьшается до подбора: так
// подобранное в тике T ускорение действует ровно SpeedDuration — в движении
// тиков T+1 … T+SpeedDuration/dt.
func (w *World) updatePickups(dt float64, ids []PlayerID) {
	for _, id := range ids {
		p := w.players[id]
		p.speedLeft = max(p.speedLeft-dt, 0)
	}
	w.collectPickups(ids)
	w.spawnPickups(dt, ids)
}

// collectPickups — игрок подбирает пикап, если их круги пересекаются.
// Игроки обходятся по ID: при одновременном касании пикап достаётся меньшему ID.
// Удаление — фильтрацией на месте, без аллокаций.
func (w *World) collectPickups(ids []PlayerID) {
	if len(w.pickups) == 0 {
		return
	}
	r := w.cfg.PlayerRadius + w.cfg.PickupRadius
	for _, id := range ids {
		p := w.players[id]
		if p.dead() {
			continue // мёртвые не подбирают
		}
		left := w.pickups[:0]
		for _, pk := range w.pickups {
			dx, dy := pk.Pos.X-p.Pos.X, pk.Pos.Y-p.Pos.Y
			if dx*dx+dy*dy < r*r {
				w.applyPickup(p, pk)
				continue
			}
			left = append(left, pk)
		}
		clear(w.pickups[len(left):])
		w.pickups = left
	}
}

// applyPickup применяет эффект. Health и ammo подбираются и при полном запасе
// (HP не превышает StartHP, патроны — MaxAmmo); повторный speed продлевает
// ускорение, а не суммирует. Оружие заменяет текущее (такое же — с полным
// запасом патронов).
func (w *World) applyPickup(p *Player, pk Pickup) {
	switch pk.Type {
	case PickupHealth:
		p.HP = min(p.HP+w.cfg.HealthAmount, StartHP)
	case PickupSpeed:
		p.speedLeft = w.cfg.SpeedDuration
	case PickupAmmo:
		p.Ammo = min(p.Ammo+w.cfg.AmmoAmount, max(w.cfg.MaxAmmo, 0))
	default:
		if id, ok := weaponOf(pk.Type); ok {
			w.giveWeapon(p, id)
		}
	}
	w.emit(Event{Type: EventPickup, Player: p.ID, Pickup: pk.Type})
}

// spawnPickups ведёт три независимых потока: health/speed (взвешенно) раз в
// PickupInterval до MaxPickups на карте, ammo раз в AmmoPickupInterval до
// MaxAmmoPickups (с поправкой на число игроков, см. ammoStream) и оружие раз в
// WeaponPickupInterval до MaxWeaponPickups — только на картах с WeaponSpots.
// Порядок потоков фиксирован — общий, ammo, оружие — ради детерминизма rng.
func (w *World) spawnPickups(dt float64, ids []PlayerID) {
	w.pickupTimer = w.spawnStream(dt, ids, w.pickupTimer, w.cfg.PickupInterval, w.cfg.MaxPickups, streamGeneral)
	interval, limit := w.ammoStream(len(ids))
	// Таймер заряжается базовым интервалом (старт, новый раунд); игроков больше —
	// отсчёт сразу укорачивается до их интервала, а не после первой коробки.
	w.ammoTimer = min(w.ammoTimer, interval)
	w.ammoTimer = w.spawnStream(dt, ids, w.ammoTimer, interval, limit, streamAmmo)
	if len(w.cfg.WeaponSpots) > 0 {
		w.weaponTimer = w.spawnStream(dt, ids, w.weaponTimer, w.cfg.WeaponPickupInterval, w.cfg.MaxWeaponPickups, streamWeapon)
	}
}

// ammoStream — интервал и лимит потока ammo для n игроков в комнате. До
// AmmoBasePlayers включительно — значения конфига как есть; дальше интервал
// сокращается в n/base раз, а лимит растёт во столько же раз (с округлением
// вверх), чтобы на игрока приходилось столько же патронов, сколько в дуэли.
func (w *World) ammoStream(n int) (interval float64, limit int) {
	interval, limit = w.cfg.AmmoPickupInterval, w.cfg.MaxAmmoPickups
	base := w.cfg.AmmoBasePlayers
	if base <= 0 || n <= base {
		return interval, limit
	}
	interval = interval * float64(base) / float64(n)
	limit = (limit*n + base - 1) / base
	return interval, limit
}

// spawnStream — один поток спавна; возвращает новое значение его таймера.
// interval ≤ 0 отключает поток. Если лимит потока на карте достигнут, спавн
// пропускается до следующего интервала; если свободной точки нет, таймер
// остаётся истёкшим и попытка повторяется на следующем тике.
func (w *World) spawnStream(dt float64, ids []PlayerID, timer, interval float64, limit int, s pickupStream) float64 {
	if interval <= 0 {
		return timer
	}
	timer -= dt
	if timer > timeEps {
		return timer
	}
	if w.countPickups(s) >= limit {
		return interval
	}
	// Тип — до точки: от него зависит зона спавна. Порядок бросков rng важен.
	var (
		typ PickupType
		pos Vec2
		ok  bool
	)
	switch s {
	case streamAmmo:
		typ = PickupAmmo
		pos, ok = w.pickupPoint(ids, typ)
	case streamWeapon:
		typ = weaponPickups[w.rng.IntN(len(weaponPickups))].typ
		pos, ok = w.weaponSpot(ids)
	default:
		typ = w.randomPickupType()
		pos, ok = w.pickupPoint(ids, typ)
	}
	if !ok {
		return timer // тип перебросится на следующей попытке
	}
	w.nextPickup++
	w.pickups = append(w.pickups, Pickup{ID: w.nextPickup, Type: typ, Pos: pos})
	return interval
}

// countPickups — сколько на карте пикапов потока s.
func (w *World) countPickups(s pickupStream) int {
	n := 0
	for _, pk := range w.pickups {
		if streamOf(pk.Type) == s {
			n++
		}
	}
	return n
}

// weaponSpot — свободная точка для оружия из Map.WeaponSpots: перебор по
// порядку среза со случайной точки (один бросок rng). Точка свободна, если
// пикапы не ближе max(PickupMinPickupDist, 2×PickupRadius), круг пикапа не
// задевает целое препятствие, а игроки не ближе PickupMinPlayerDist.
func (w *World) weaponSpot(ids []PlayerID) (Vec2, bool) {
	spots := w.cfg.WeaponSpots
	r := w.cfg.PickupRadius
	gap := max(w.cfg.PickupMinPickupDist, 2*r)
	start := w.rng.IntN(len(spots))
	for i := range spots {
		pos := spots[(start+i)%len(spots)]
		if w.circleFree(pos, r) && !w.pickupWithin(pos, gap) && !w.nearPlayer(pos, ids) {
			return pos, true
		}
	}
	return Vec2{}, false
}

// pickupPoint ищет случайную точку для пикапа типа typ: в одной из его зон
// карты (Map.PickupZones), а без зон — где угодно. Круг пикапа не задевает
// препятствия; центр не ближе max(PickupRadius, PickupEdgeMargin) к краю мира,
// не ближе PickupMinPlayerDist к центру любого игрока и не ближе
// PickupMinPickupDist к центру любого лежащего пикапа (всех потоков).
func (w *World) pickupPoint(ids []PlayerID, typ PickupType) (Vec2, bool) {
	r := w.cfg.PickupRadius
	m := max(r, w.cfg.PickupEdgeMargin) // builtin max для float инлайнится, в отличие от math.Max
	zones := w.cfg.PickupZones[typ]
	for range pickupAttempts {
		var pos Vec2
		if len(zones) > 0 {
			pos = zones[w.rng.IntN(len(zones))].sample(w.rng)
			if pos.X < m || pos.X > w.cfg.Width-m || pos.Y < m || pos.Y > w.cfg.Height-m {
				continue
			}
		} else {
			pos = Vec2{
				X: m + w.rng.Float64()*(w.cfg.Width-2*m),
				Y: m + w.rng.Float64()*(w.cfg.Height-2*m),
			}
		}
		if w.circleFree(pos, r) && !w.nearPickup(pos) && !w.nearPlayer(pos, ids) {
			return pos, true
		}
	}
	return Vec2{}, false
}

// nearPlayer — стоит ли какой-либо игрок ближе PickupMinPlayerDist к точке.
func (w *World) nearPlayer(pos Vec2, ids []PlayerID) bool {
	minD2 := w.cfg.PickupMinPlayerDist * w.cfg.PickupMinPlayerDist
	for _, id := range ids {
		pp := w.players[id].Pos
		if dx, dy := pos.X-pp.X, pos.Y-pp.Y; dx*dx+dy*dy < minD2 {
			return true
		}
	}
	return false
}

// nearPickup — лежит ли какой-либо пикап ближе PickupMinPickupDist к точке.
func (w *World) nearPickup(pos Vec2) bool {
	d := w.cfg.PickupMinPickupDist
	if d <= 0 {
		return false
	}
	return w.pickupWithin(pos, d)
}

// pickupWithin — лежит ли какой-либо пикап ближе d к точке.
func (w *World) pickupWithin(pos Vec2, d float64) bool {
	for _, pk := range w.pickups {
		if dx, dy := pos.X-pk.Pos.X, pos.Y-pk.Pos.Y; dx*dx+dy*dy < d*d {
			return true
		}
	}
	return false
}

// resetPickups — новый раунд: пикапы убираются, отсчёт до спавна начинается заново.
func (w *World) resetPickups() {
	clear(w.pickups)
	w.pickups = w.pickups[:0]
	w.pickupTimer = w.cfg.PickupInterval
	w.ammoTimer = w.cfg.AmmoPickupInterval
	w.weaponTimer = w.cfg.WeaponPickupInterval
}

// speedFactor — множитель скорости игрока с учётом ускорения.
func (w *World) speedFactor(p *Player) float64 {
	if p.speedLeft > timeEps {
		return w.cfg.SpeedMultiplier
	}
	return 1
}
