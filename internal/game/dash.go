package game

import "math"

// displacement — смещение игрока за тик: рывок, если он идёт, иначе обычное
// управление (с учётом ускорения от пикапа). Коллизии применяет move.
//
// Рывок стартует по Input.Dash, если кулдаун готов и рывок не идёт. Запрос
// одноразовый: гасится в конце тика, так что потерянный следующий ввод
// не вызовет повторного рывка.
func (w *World) displacement(p *Player, dt float64) Vec2 {
	p.dashCd = max(p.dashCd-dt, 0)
	if p.dashReq && p.dashLeft <= timeEps && p.dashCd <= timeEps && w.cfg.DashDuration > 0 {
		w.startDash(p)
	}
	p.dashReq = false

	if p.dashLeft > timeEps {
		return w.dashStep(p, dt) // обычное управление во время рывка не действует
	}
	speed := w.cfg.PlayerSpeed * w.speedFactor(p)
	return Vec2{X: p.Move.X * speed * dt, Y: p.Move.Y * speed * dt}
}

// startDash задаёт направление рывка: текущее движение, а если игрок стоит —
// направление взгляда.
func (w *World) startDash(p *Player) {
	if l := math.Hypot(p.Move.X, p.Move.Y); l > 0 {
		p.dashDir = Vec2{X: p.Move.X / l, Y: p.Move.Y / l}
	} else {
		p.dashDir = Vec2{X: math.Cos(p.Angle), Y: math.Sin(p.Angle)}
	}
	p.dashLeft = w.cfg.DashDuration
	p.dashCd = w.cfg.DashCooldown
}

// dashStep — путь рывка за тик. Скорость падает линейно до нуля:
// v(τ) = DashSpeed·τ/DashDuration, где τ — оставшееся время рывка.
// Путь за шаг — точный интеграл v на [τ−dt, τ], поэтому весь рывок проходит
// DashSpeed·DashDuration/2 независимо от длины тика.
func (w *World) dashStep(p *Player, dt float64) Vec2 {
	left := p.dashLeft
	next := max(left-dt, 0)
	dist := w.cfg.DashSpeed * (left*left - next*next) / (2 * w.cfg.DashDuration)
	p.dashLeft = next
	return Vec2{X: p.dashDir.X * dist, Y: p.dashDir.Y * dist}
}

// dashCooldownLeft — оставшийся кулдаун для снапшота; 0, если рывок готов.
func (p *Player) dashCooldownLeft() float64 {
	if p.dashCd <= timeEps {
		return 0
	}
	return p.dashCd
}
