package game

// EventType — вид одномоментного события.
type EventType string

const (
	EventHit       EventType = "hit"       // пуля попала в игрока: Pos, Target, Shooter
	EventKill      EventType = "kill"      // попадание убило цель: Target, Shooter
	EventShot      EventType = "shot"      // выстрел: Player, Pos (точка вылета)
	EventBulletHit EventType = "bulletHit" // пуля ударилась о стену, границу мира или неуязвимого игрока: Pos
	EventPickup    EventType = "pickup"    // игрок подобрал пикап: Player, Pickup
	EventRespawn   EventType = "respawn"   // игрок возродился: Player, Pos

	EventCrateHit    EventType = "crateHit"    // пуля попала в целый ящик: Obstacle, HP (остаток), Pos
	EventCrateBroken EventType = "crateBroken" // ящик сломан (после своего crateHit): Obstacle, Pos (центр ящика)

	EventZombieKill EventType = "zombieKill" // пуля убила зомби: Zombie, Shooter, Pos (где стоял зомби)
)

// Event — одномоментное событие тика. Снапшот передаёт состояние, а не то,
// что произошло между снапшотами (пуля, попавшая в цель, в снапшот уже не
// попадает), поэтому такие вещи клиенту сообщают события.
//
// События накапливаются за тик и сбрасываются в начале следующего Update:
// каждое попадает ровно в один тик. Порядок — порядок возникновения
// (детерминирован: игроки и пули обходятся в фиксированном порядке).
type Event struct {
	Type     EventType
	Pos      Vec2       // hit, shot, bulletHit, respawn, crateHit, crateBroken, zombieKill
	Target   PlayerID   // hit, kill
	Shooter  PlayerID   // hit, kill, zombieKill; 0 в hit/kill — урон от зомби
	Player   PlayerID   // shot, pickup, respawn
	Pickup   PickupType // pickup
	Obstacle int        // crateHit, crateBroken: индекс в cfg.Obstacles
	HP       int        // crateHit: прочность ящика после попадания
	Zombie   ZombieID   // zombieKill
}

func (w *World) emit(e Event) { w.events = append(w.events, e) }

// exitPoint — точка, где отрезок from → to (from внутри мира) пересекает его границу.
func (w *World) exitPoint(from, to Vec2) Vec2 {
	t := 1.0
	axis := func(p, q, hi float64) {
		switch {
		case q < 0:
			t = min(t, (0-p)/(q-p))
		case q > hi:
			t = min(t, (hi-p)/(q-p))
		}
	}
	axis(from.X, to.X, w.cfg.Width)
	axis(from.Y, to.Y, w.cfg.Height)
	return Vec2{X: from.X + (to.X-from.X)*t, Y: from.Y + (to.Y-from.Y)*t}
}
