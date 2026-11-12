package game

// PickupType — вид пикапа.
type PickupType string

const (
	PickupHealth PickupType = "health" // +HealthAmount hp, не выше StartHP
	PickupSpeed  PickupType = "speed"  // ×SpeedMultiplier к скорости на SpeedDuration
	PickupAmmo   PickupType = "ammo"   // +AmmoAmount патронов, не выше MaxAmmo
)

// pickupWeights — веса типов в общем потоке спавна (в процентах, сумма
// pickupWeightTotal). Ammo сюда не входит — у него свой поток (AmmoPickupInterval).
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
// ускорение, а не суммирует.
func (w *World) applyPickup(p *Player, pk Pickup) {
	switch pk.Type {
	case PickupHealth:
		p.HP = min(p.HP+w.cfg.HealthAmount, StartHP)
	case PickupSpeed:
		p.speedLeft = w.cfg.SpeedDuration
	case PickupAmmo:
		p.Ammo = min(p.Ammo+w.cfg.AmmoAmount, max(w.cfg.MaxAmmo, 0))
	}
	w.emit(Event{Type: EventPickup, Player: p.ID, Pickup: pk.Type})
}

// spawnPickups ведёт два независимых потока: health/speed (взвешенно) раз в
// PickupInterval до MaxPickups на карте и ammo раз в AmmoPickupInterval до
// MaxAmmoPickups (с поправкой на число игроков, см. ammoStream). Порядок
// потоков фиксирован — общий, затем ammo — ради детерминизма rng.
func (w *World) spawnPickups(dt float64, ids []PlayerID) {
	w.pickupTimer = w.spawnStream(dt, ids, w.pickupTimer, w.cfg.PickupInterval, w.cfg.MaxPickups, false)
	interval, limit := w.ammoStream(len(ids))
	// Таймер заряжается базовым интервалом (старт, новый раунд); игроков больше —
	// отсчёт сразу укорачивается до их интервала, а не после первой коробки.
	w.ammoTimer = min(w.ammoTimer, interval)
	w.ammoTimer = w.spawnStream(dt, ids, w.ammoTimer, interval, limit, true)
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
func (w *World) spawnStream(dt float64, ids []PlayerID, timer, interval float64, limit int, ammo bool) float64 {
	if interval <= 0 {
		return timer
	}
	timer -= dt
	if timer > timeEps {
		return timer
	}
	if w.countPickups(ammo) >= limit {
		return interval
	}
	// Тип — до точки: от него зависит зона спавна. Порядок бросков rng важен.
	typ := PickupAmmo
	if !ammo {
		typ = w.randomPickupType()
	}
	pos, ok := w.pickupPoint(ids, typ)
	if !ok {
		return timer // тип перебросится на следующей попытке
	}
	w.nextPickup++
	w.pickups = append(w.pickups, Pickup{ID: w.nextPickup, Type: typ, Pos: pos})
	return interval
}

// countPickups — сколько на карте пикапов потока ammo (ammo = true) или общего.
func (w *World) countPickups(ammo bool) int {
	n := 0
	for _, pk := range w.pickups {
		if (pk.Type == PickupAmmo) == ammo {
			n++
		}
	}
	return n
}

// pickupPoint ищет случайную точку для пикапа типа typ: в одной из его зон
// карты (Map.PickupZones), а без зон — где угодно. Круг пикапа не задевает
// препятствия; центр не ближе max(PickupRadius, PickupEdgeMargin) к краю мира,
// не ближе PickupMinPlayerDist к центру любого игрока и не ближе
// PickupMinPickupDist к центру любого лежащего пикапа (обоих потоков).
func (w *World) pickupPoint(ids []PlayerID, typ PickupType) (Vec2, bool) {
	r := w.cfg.PickupRadius
	m := max(r, w.cfg.PickupEdgeMargin) // builtin max для float инлайнится, в отличие от math.Max
	minD2 := w.cfg.PickupMinPlayerDist * w.cfg.PickupMinPlayerDist
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
		if !w.circleFree(pos, r) || w.nearPickup(pos) {
			continue
		}
		near := false
		for _, id := range ids {
			pp := w.players[id].Pos
			if dx, dy := pos.X-pp.X, pos.Y-pp.Y; dx*dx+dy*dy < minD2 {
				near = true
				break
			}
		}
		if !near {
			return pos, true
		}
	}
	return Vec2{}, false
}

// nearPickup — лежит ли какой-либо пикап ближе PickupMinPickupDist к точке.
func (w *World) nearPickup(pos Vec2) bool {
	d := w.cfg.PickupMinPickupDist
	if d <= 0 {
		return false
	}
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
}

// speedFactor — множитель скорости игрока с учётом ускорения.
func (w *World) speedFactor(p *Player) float64 {
	if p.speedLeft > timeEps {
		return w.cfg.SpeedMultiplier
	}
	return 1
}
