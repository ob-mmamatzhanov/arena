package game

// Разрушаемые ящики. Прочность хранится в World.crateHP параллельно
// cfg.Obstacles (сам срез препятствий не меняется — он общий с картой):
//
//	crateSolid (−1) — неразрушимое препятствие (кирпич, или CrateHits ≤ 0);
//	> 0             — целый ящик, столько попаданий он ещё выдержит;
//	0               — сломан: для движения, пуль и спавна пикапов его нет.
//
// Каждая пуля, попавшая в целый ящик, гасится и снимает 1 прочности; на нуле
// ящик ломается. В начале раунда все ящики восстанавливаются — в этот момент
// игроки стоят на спавнах (не ближе 60 px к препятствиям), а пули и пикапы
// убраны, так что восстановленный ящик никого не зажмёт.

const crateSolid = -1

// CrateState — прочность повреждённого или сломанного ящика для снапшота.
// Index — индекс препятствия в cfg.Obstacles (и в Welcome.obstacles).
type CrateState struct {
	Index int
	HP    int // 0 — сломан
}

// newCrateHP — начальная прочность всех препятствий карты.
func newCrateHP(cfg Config) []int {
	hp := make([]int, len(cfg.Obstacles))
	fillCrateHP(hp, cfg)
	return hp
}

func fillCrateHP(hp []int, cfg Config) {
	for i, o := range cfg.Obstacles {
		if cfg.CrateHits > 0 && o.KindOrDefault() == ObstacleCrate {
			hp[i] = cfg.CrateHits
		} else {
			hp[i] = crateSolid
		}
	}
}

// resetCrates — новый раунд: все ящики снова целые.
func (w *World) resetCrates() {
	fillCrateHP(w.crateHP, w.cfg)
	w.zg.flow.dirty = true // проходимость клеток для зомби изменилась
}

// broken — сломан ли i-й элемент cfg.Obstacles (тогда его нет для коллизий).
func (w *World) broken(i int) bool { return i < len(w.crateHP) && w.crateHP[i] == 0 }

// destructible — целый разрушаемый ящик.
func (w *World) destructible(i int) bool { return i < len(w.crateHP) && w.crateHP[i] > 0 }

// damageCrate — пуля попала в целый ящик i в точке at: −1 прочности,
// событие crateHit (с остатком), на нуле — ещё crateBroken (центр ящика).
func (w *World) damageCrate(i int, at Vec2) {
	w.crateHP[i]--
	w.emit(Event{Type: EventCrateHit, Pos: at, Obstacle: i, HP: w.crateHP[i]})
	if w.crateHP[i] == 0 {
		o := w.cfg.Obstacles[i]
		w.emit(Event{Type: EventCrateBroken, Pos: Vec2{X: o.X + o.W/2, Y: o.Y + o.H/2}, Obstacle: i})
		w.zg.flow.dirty = true // проходимость клеток для зомби изменилась
	}
}

// crateStates — повреждённые и сломанные ящики (целые не передаются: клиент
// считает целым всё, чего нет в списке). nil, если таких нет.
func (w *World) crateStates() []CrateState {
	var out []CrateState
	for i, hp := range w.crateHP {
		if hp >= 0 && hp < w.cfg.CrateHits {
			out = append(out, CrateState{Index: i, HP: hp})
		}
	}
	return out
}
