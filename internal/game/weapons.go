package game

// Оружие. У игрока всегда одно оружие (Player.Weapon). Пистолет — оружие по
// умолчанию: с ним игрок появляется, к нему возвращается после смерти, в
// начале раунда и когда кончились патроны подобранного оружия. Пистолет
// стреляет общими патронами (Player.Ammo, Config.MaxAmmo), остальное оружие —
// своими (Player.WeaponAmmo), их выдаёт пикап оружия (AmmoOnPickup).
// Подобранное оружие заменяет текущее.
//
// Выстрел — это Pellets пуль сразу (дробовик — веер), один кулдаун, один
// патрон, одна прибавка перегрева и одно событие shot. Каждая пуля несёт урон
// своего оружия (Bullet.Damage).

// WeaponID — вид оружия, индекс в Config.Weapons.
type WeaponID uint8

const (
	WeaponPistol WeaponID = iota
	WeaponShotgun
	WeaponSMG
	WeaponRifle

	NumWeapons // число видов оружия
)

var weaponNames = [NumWeapons]string{"pistol", "shotgun", "smg", "rifle"}

// String — имя оружия в протоколе: pistol, shotgun, smg, rifle.
func (id WeaponID) String() string {
	if id < NumWeapons {
		return weaponNames[id]
	}
	return "unknown"
}

// Weapon — параметры оружия.
type Weapon struct {
	ID          WeaponID
	Damage      int     // урон одной пули (дробины)
	CooldownMs  int     // минимальный интервал между выстрелами, мс (на деле — целое число тиков, не меньше)
	BulletSpeed float64 // единиц в секунду
	BulletTTL   float64 // время жизни пули, секунды
	Pellets     int     // пуль за выстрел; ≤ 1 — одна
	// SpreadRad — полуугол веера: Pellets пуль летят равномерно от −SpreadRad
	// до +SpreadRad относительно направления выстрела (у одной пули — без
	// отклонения). Разброс от перегрева сдвигает весь веер целиком.
	SpreadRad   float64
	HeatPerShot float64 // прибавка перегрева за выстрел (см. Config.HeatDecay, MaxSpread)
	// AmmoOnPickup — выстрелов в подобранном оружии. У пистолета 0: он
	// стреляет общими патронами и как пикап не появляется.
	AmmoOnPickup int
}

// cooldown — кулдаун в секундах.
func (wp *Weapon) cooldown() float64 { return float64(wp.CooldownMs) / 1000 }

// pellets — пуль за выстрел (не меньше одной).
func (wp *Weapon) pellets() int { return max(wp.Pellets, 1) }

// pelletOffset — отклонение k-й из n пуль веера от направления выстрела.
func (wp *Weapon) pelletOffset(k, n int) float64 {
	if n <= 1 {
		return 0
	}
	return wp.SpreadRad * (2*float64(k)/float64(n-1) - 1)
}

// DefaultWeapons — таблица оружия продакшена.
func DefaultWeapons() [NumWeapons]Weapon {
	return [NumWeapons]Weapon{
		WeaponPistol: {ID: WeaponPistol, Damage: 25, CooldownMs: 200, BulletSpeed: 500, BulletTTL: 1.5,
			Pellets: 1, HeatPerShot: 0.2},
		WeaponShotgun: {ID: WeaponShotgun, Damage: 16, CooldownMs: 650, BulletSpeed: 600, BulletTTL: 0.5,
			Pellets: 6, SpreadRad: 0.17, HeatPerShot: 0.4, AmmoOnPickup: 8},
		WeaponSMG: {ID: WeaponSMG, Damage: 17, CooldownMs: 90, BulletSpeed: 550, BulletTTL: 1.2,
			Pellets: 1, HeatPerShot: 0.12, AmmoOnPickup: 40},
		WeaponRifle: {ID: WeaponRifle, Damage: 70, CooldownMs: 1300, BulletSpeed: 1100, BulletTTL: 1.5,
			Pellets: 1, AmmoOnPickup: 5},
	}
}

// weapon — параметры текущего оружия игрока.
func (w *World) weapon(p *Player) *Weapon { return &w.cfg.Weapons[p.Weapon] }

// resetWeapon — игрок снова с пистолетом (смерть, новый раунд).
func (p *Player) resetWeapon() { p.Weapon, p.WeaponAmmo = WeaponPistol, 0 }

// giveWeapon — подобранное оружие заменяет текущее с полным запасом патронов.
func (w *World) giveWeapon(p *Player, id WeaponID) {
	p.Weapon, p.WeaponAmmo = id, w.cfg.Weapons[id].AmmoOnPickup
	if id == WeaponPistol || p.WeaponAmmo <= 0 {
		p.resetWeapon()
	}
}

// hasAmmo — может ли игрок выстрелить по патронам. Пистолет при MaxAmmo ≤ 0
// стреляет без ограничений.
func (w *World) hasAmmo(p *Player) bool {
	if p.Weapon != WeaponPistol {
		return p.WeaponAmmo > 0
	}
	return w.cfg.MaxAmmo <= 0 || p.Ammo > 0
}

// useAmmo списывает патрон за выстрел. Последний патрон подобранного оружия —
// возврат к пистолету.
func (w *World) useAmmo(p *Player) {
	switch {
	case p.Weapon != WeaponPistol:
		if p.WeaponAmmo--; p.WeaponAmmo <= 0 {
			p.resetWeapon()
		}
	case w.cfg.MaxAmmo > 0:
		p.Ammo--
	}
}
