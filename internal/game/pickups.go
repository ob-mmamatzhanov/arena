package game

// PickupType — вид пикапа.
type PickupType string

const (
	PickupHealth PickupType = "health" // +HealthAmount hp, не выше StartHP
	PickupSpeed  PickupType = "speed"  // ×SpeedMultiplier к скорости на SpeedDuration
)

// pickupTypes — порядок важен: rng выбирает индекс, смена порядка меняет симуляцию.
var pickupTypes = [...]PickupType{PickupHealth, PickupSpeed}

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

// applyPickup применяет эффект. Health подбирается и при полном HP (HP не
// превышает StartHP); повторный speed продлевает ускорение, а не суммирует.
func (w *World) applyPickup(p *Player, pk Pickup) {
	switch pk.Type {
	case PickupHealth:
		p.HP = min(p.HP+w.cfg.HealthAmount, StartHP)
	case PickupSpeed:
		p.speedLeft = w.cfg.SpeedDuration
	}
	w.emit(Event{Type: EventPickup, Player: p.ID, Pickup: pk.Type})
}

// spawnPickups раз в PickupInterval кладёт пикап случайного типа, если на карте
// их меньше MaxPickups. PickupInterval ≤ 0 отключает спавн.
func (w *World) spawnPickups(dt float64, ids []PlayerID) {
	if w.cfg.PickupInterval <= 0 {
		return
	}
	w.pickupTimer -= dt
	if w.pickupTimer > timeEps {
		return
	}
	if len(w.pickups) >= w.cfg.MaxPickups {
		w.pickupTimer = w.cfg.PickupInterval // лимит: пропускаем этот спавн
		return
	}
	pos, ok := w.pickupPoint(ids)
	if !ok {
		return // таймер остаётся истёкшим — попробуем на следующем тике
	}
	w.nextPickup++
	w.pickups = append(w.pickups, Pickup{
		ID:   w.nextPickup,
		Type: pickupTypes[w.rng.IntN(len(pickupTypes))],
		Pos:  pos,
	})
	w.pickupTimer = w.cfg.PickupInterval
}

// pickupPoint ищет случайную точку, где круг пикапа не задевает препятствия,
// а до центра любого игрока не меньше PickupMinPlayerDist.
func (w *World) pickupPoint(ids []PlayerID) (Vec2, bool) {
	r := w.cfg.PickupRadius
	minD2 := w.cfg.PickupMinPlayerDist * w.cfg.PickupMinPlayerDist
	for range pickupAttempts {
		pos := Vec2{
			X: r + w.rng.Float64()*(w.cfg.Width-2*r),
			Y: r + w.rng.Float64()*(w.cfg.Height-2*r),
		}
		if !w.circleFree(pos, r) {
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

// resetPickups — новый раунд: пикапы убираются, отсчёт до спавна начинается заново.
func (w *World) resetPickups() {
	clear(w.pickups)
	w.pickups = w.pickups[:0]
	w.pickupTimer = w.cfg.PickupInterval
}

// speedFactor — множитель скорости игрока с учётом ускорения.
func (w *World) speedFactor(p *Player) float64 {
	if p.speedLeft > timeEps {
		return w.cfg.SpeedMultiplier
	}
	return 1
}
