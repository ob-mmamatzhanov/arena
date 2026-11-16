package game

import (
	"math"
	"testing"
)

// armed — мир testConfig с игроком 1 в (500, 500) с оружием id.
func armed(t *testing.T, cfg Config, id WeaponID) *World {
	t.Helper()
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	w.giveWeapon(w.players[1], id)
	return w
}

// Таблица продакшена — значения из постановки, ID совпадает с индексом.
func TestDefaultWeaponsTable(t *testing.T) {
	want := map[string]Weapon{
		"pistol":  {ID: WeaponPistol, Damage: 25, CooldownMs: 200, BulletSpeed: 500, BulletTTL: 1.5, Pellets: 1, SpreadRad: 0, HeatPerShot: 0.2, AmmoOnPickup: 0},
		"shotgun": {ID: WeaponShotgun, Damage: 16, CooldownMs: 650, BulletSpeed: 600, BulletTTL: 0.5, Pellets: 6, SpreadRad: 0.17, HeatPerShot: 0.4, AmmoOnPickup: 8},
		"smg":     {ID: WeaponSMG, Damage: 17, CooldownMs: 90, BulletSpeed: 550, BulletTTL: 1.2, Pellets: 1, SpreadRad: 0, HeatPerShot: 0.12, AmmoOnPickup: 40},
		"rifle":   {ID: WeaponRifle, Damage: 70, CooldownMs: 1300, BulletSpeed: 1100, BulletTTL: 1.5, Pellets: 1, SpreadRad: 0, HeatPerShot: 0, AmmoOnPickup: 5},
	}
	table := DefaultWeapons()
	for i, wp := range table {
		if wp.ID != WeaponID(i) {
			t.Errorf("оружие %d с ID %d", i, wp.ID)
		}
		if got, ok := want[wp.ID.String()]; !ok || got != wp {
			t.Errorf("%s = %+v, want %+v", wp.ID, wp, got)
		}
	}
	if len(table) != len(want) {
		t.Errorf("оружия %d, want %d", len(table), len(want))
	}
	if DefaultConfig().Weapons != table {
		t.Error("DefaultConfig не берёт DefaultWeapons")
	}
}

// Дробовик: один выстрел — 6 пуль веером ±SpreadRad с равным шагом вокруг
// направления взгляда, с уроном, скоростью и временем жизни дробовика; одно
// событие shot с оружием, один патрон.
func TestShotgunFiresSixPelletFan(t *testing.T) {
	cfg := testConfig()
	w := armed(t, cfg, WeaponShotgun)
	sg := cfg.Weapons[WeaponShotgun]
	const aim = 0.7
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, Angle: aim, Fire: true}})

	if len(w.bullets) != 6 {
		t.Fatalf("пуль после выстрела %d, want 6", len(w.bullets))
	}
	for k, b := range w.bullets {
		want := aim - sg.SpreadRad + 2*sg.SpreadRad*float64(k)/5 // от −SpreadRad до +SpreadRad, равный шаг
		if got := math.Atan2(b.Vel.Y, b.Vel.X); math.Abs(got-want) > 1e-9 {
			t.Errorf("дробина %d под углом %.4f, want %.4f", k, got, want)
		}
		if v := math.Hypot(b.Vel.X, b.Vel.Y); math.Abs(v-sg.BulletSpeed) > 1e-9 {
			t.Errorf("дробина %d со скоростью %.1f, want %.1f", k, v, sg.BulletSpeed)
		}
		if b.Damage != sg.Damage || b.ttl != sg.BulletTTL {
			t.Errorf("дробина %d: урон %d, ttl %.2f; want %d, %.2f", k, b.Damage, b.ttl, sg.Damage, sg.BulletTTL)
		}
	}
	shots := 0
	for _, e := range w.events {
		if e.Type == EventShot {
			shots++
			if e.Weapon != WeaponShotgun {
				t.Errorf("shot с оружием %s, want shotgun", e.Weapon)
			}
		}
	}
	if shots != 1 {
		t.Errorf("событий shot %d, want 1 на весь веер", shots)
	}
	if p := w.players[1]; p.WeaponAmmo != sg.AmmoOnPickup-1 || !approx(p.Heat, sg.HeatPerShot) {
		t.Errorf("после выстрела патронов %d, heat %.2f; want %d, %.2f", p.WeaponAmmo, p.Heat, sg.AmmoOnPickup-1, sg.HeatPerShot)
	}
}

// Дробины летят 0.5 с со скоростью 600 (300 px): цель в 60 px, где веер
// ±0.17 рад ещё уже хитбокса (±12 px), получает весь заряд, в 350 px — ничего.
func TestShotgunRange(t *testing.T) {
	sg := DefaultWeapons()[WeaponShotgun]
	for _, tc := range []struct {
		dist float64
		hit  bool
	}{{60, true}, {350, false}} {
		w := armed(t, testConfig(), WeaponShotgun)
		w.AddPlayer(2)
		place(w, 2, 500+tc.dist, 500)
		fireOnce(w, 1, 0)
		tickN(w, 20)
		lost := StartHP - w.players[2].HP
		if full := min(sg.Pellets*sg.Damage, StartHP); tc.hit && lost != full {
			t.Errorf("цель в %.0f px потеряла %d hp, want весь заряд %d", tc.dist, lost, full)
		}
		if !tc.hit && lost != 0 {
			t.Errorf("цель в %.0f px потеряла %d hp, want 0: дробь не долетает", tc.dist, lost)
		}
	}
}

// Пуля несёт урон своего оружия: попадание из smg снимает 17 hp, а не 25.
func TestBulletCarriesWeaponDamage(t *testing.T) {
	w := armed(t, testConfig(), WeaponSMG)
	w.AddPlayer(2)
	place(w, 2, 700, 500)
	fireOnce(w, 1, 0)
	hits := runCollectHits(w, 20)
	if len(hits) != 1 || w.players[2].HP != StartHP-17 {
		t.Fatalf("попаданий %d, hp цели %d; want 1, %d", len(hits), w.players[2].HP, StartHP-17)
	}
}

// Винтовка убивает зомби (30 hp) с одного попадания, пистолет (25) — нет.
func TestRifleKillsZombieInOneHit(t *testing.T) {
	for _, tc := range []struct {
		weapon WeaponID
		kills  bool
	}{{WeaponRifle, true}, {WeaponPistol, false}} {
		w := armed(t, zombieConfig(), tc.weapon)
		w.players[1].invulnLeft = 0
		z := putZombie(w, 800, 500)
		id := z.ID
		fireOnce(w, 1, 0)
		killed := false
		for range 20 {
			if _, ok := findEvent(w.events, EventZombieKill); ok {
				killed = true
				break
			}
			w.Update(dt, nil)
		}
		if killed != tc.kills {
			t.Errorf("%s: зомби %d убит с одного попадания = %v, want %v", tc.weapon, id, killed, tc.kills)
		}
	}
}

// Кулдаун: при зажатом огне выстрелы идут через ⌈CooldownMs / dt⌉ тиков —
// не чаще кулдауна; у smg 90 мс при тике 50 мс — раз в 2 тика.
func TestWeaponCooldowns(t *testing.T) {
	want := map[WeaponID]int{WeaponPistol: 4, WeaponShotgun: 13, WeaponSMG: 2, WeaponRifle: 26}
	for id := range NumWeapons {
		cfg := testConfig()
		cfg.Weapons[id].AmmoOnPickup = 1000 // патроны не кончаются за прогон
		w := armed(t, cfg, id)
		var shotTicks []int
		for i := range 100 {
			w.Update(dt, map[PlayerID]Input{1: {Seq: uint32(i + 1), Fire: true}})
			for _, e := range w.events {
				if e.Type == EventShot {
					shotTicks = append(shotTicks, i)
				}
			}
		}
		if len(shotTicks) < 3 {
			t.Fatalf("%s: выстрелов %d за 5 с", id, len(shotTicks))
		}
		for k := 1; k < len(shotTicks); k++ {
			gap := shotTicks[k] - shotTicks[k-1]
			if gap != want[id] {
				t.Fatalf("%s: между выстрелами %d и %d — %d тиков, want %d", id, k-1, k, gap, want[id])
			}
			if float64(gap)*dt < cfg.Weapons[id].cooldown()-timeEps {
				t.Fatalf("%s: выстрелы через %.2f с, кулдаун %.2f", id, float64(gap)*dt, cfg.Weapons[id].cooldown())
			}
		}
	}
}

// Последний патрон подобранного оружия — возврат к пистолету; общие патроны
// пистолета, пока стреляла винтовка, не тратились.
func TestWeaponRunsDryBackToPistol(t *testing.T) {
	cfg := testConfig()
	cfg.MaxAmmo = 30
	w := armed(t, cfg, WeaponRifle)
	p := w.players[1]
	p.Ammo = 12
	var fired []WeaponID
	for i := range 200 {
		w.Update(dt, map[PlayerID]Input{1: {Seq: uint32(i + 1), Fire: true}})
		for _, e := range w.events {
			if e.Type == EventShot {
				fired = append(fired, e.Weapon)
			}
		}
		if len(fired) == 5 { // последний патрон винтовки
			if p.Weapon != WeaponPistol || p.WeaponAmmo != 0 || p.Ammo != 12 {
				t.Fatalf("после 5-го выстрела: %s, патронов %d, общих %d; want pistol, 0, 12", p.Weapon, p.WeaponAmmo, p.Ammo)
			}
			if s := playerState(t, w, 1); s.Weapon != WeaponPistol || s.WeaponAmmo != 0 {
				t.Fatalf("в снапшоте %s, %d; want pistol, 0", s.Weapon, s.WeaponAmmo)
			}
		}
		if len(fired) == 6 {
			break
		}
	}
	if len(fired) != 6 {
		t.Fatalf("выстрелов %d, want 6", len(fired))
	}
	for k, id := range fired {
		if want := map[bool]WeaponID{true: WeaponRifle, false: WeaponPistol}[k < 5]; id != want {
			t.Errorf("выстрел %d из %s, want %s", k, id, want)
		}
	}
	if p.Ammo != 11 {
		t.Errorf("общих патронов после выстрела пистолета %d, want 11", p.Ammo)
	}
}

// Снапшот показывает оружие и его патроны.
func TestSnapshotWeapon(t *testing.T) {
	w := armed(t, testConfig(), WeaponSMG)
	if s := playerState(t, w, 1); s.Weapon != WeaponSMG || s.WeaponAmmo != 40 {
		t.Fatalf("снапшот: %s, %d; want smg, 40", s.Weapon, s.WeaponAmmo)
	}
	w.AddPlayer(2)
	if s := playerState(t, w, 2); s.Weapon != WeaponPistol || s.WeaponAmmo != 0 {
		t.Fatalf("вошедший игрок: %s, %d; want pistol, 0", s.Weapon, s.WeaponAmmo)
	}
}

// Смерть от пули сбрасывает оружие к пистолету — и с задержкой респауна,
// и с мгновенным.
func TestDeathResetsWeapon(t *testing.T) {
	for _, delay := range []float64{3, 0} {
		cfg := deathConfig()
		cfg.RespawnDelay = delay
		w := newDuel(t, cfg)
		w.giveWeapon(w.players[2], WeaponShotgun)
		killTarget(t, w)
		if p := w.players[2]; p.Weapon != WeaponPistol || p.WeaponAmmo != 0 {
			t.Errorf("задержка %.0f: убитый с %s (%d патронов), want pistol", delay, p.Weapon, p.WeaponAmmo)
		}
	}
}

// Смерть от зомби — тоже к пистолету.
func TestZombieDeathResetsWeapon(t *testing.T) {
	w := armed(t, zombieConfig(), WeaponRifle)
	p := w.players[1]
	p.invulnLeft, p.HP = 0, 1
	putZombie(w, 510, 500)
	w.Update(dt, nil)
	if !p.dead() || p.Weapon != WeaponPistol || p.WeaponAmmo != 0 {
		t.Fatalf("укушенный насмерть: dead=%v, %s (%d); want pistol", p.dead(), p.Weapon, p.WeaponAmmo)
	}
}

// Новый раунд — все снова с пистолетами.
func TestNewRoundResetsWeapon(t *testing.T) {
	cfg := roundConfig(1, 0)
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.giveWeapon(w.players[1], WeaponSMG)
	tickN(w, ticksFor(cfg.RoundTime+cfg.IntermissionTime)+1)
	if w.phase != PhasePlay {
		t.Fatalf("фаза %s, want новый раунд", w.phase)
	}
	if p := w.players[1]; p.Weapon != WeaponPistol || p.WeaponAmmo != 0 {
		t.Fatalf("в новом раунде %s (%d), want pistol", p.Weapon, p.WeaponAmmo)
	}
}

// Подобранное оружие заменяет текущее с полным запасом патронов; событие
// pickup — с типом пикапа.
func TestWeaponPickupReplacesWeapon(t *testing.T) {
	w := armed(t, testConfig(), WeaponSMG)
	p := w.players[1]
	p.WeaponAmmo = 3
	for _, tc := range []struct {
		typ  PickupType
		want WeaponID
	}{{PickupRifle, WeaponRifle}, {PickupShotgun, WeaponShotgun}, {PickupShotgun, WeaponShotgun}, {PickupSMG, WeaponSMG}} {
		dropPickup(w, tc.typ, 505, 500)
		w.Update(dt, nil)
		if p.Weapon != tc.want || p.WeaponAmmo != w.cfg.Weapons[tc.want].AmmoOnPickup {
			t.Fatalf("после %s: %s (%d), want %s (%d)", tc.typ, p.Weapon, p.WeaponAmmo, tc.want, w.cfg.Weapons[tc.want].AmmoOnPickup)
		}
		if e, ok := findEvent(w.events, EventPickup); !ok || e.Pickup != tc.typ || e.Player != 1 {
			t.Fatalf("событие pickup = %+v, %v; want %s от 1", e, ok, tc.typ)
		}
		p.WeaponAmmo = 1 // такое же оружие тоже даёт полный запас
	}
}

// Оружие появляется раз в 30 с в точках карты, не больше двух сразу.
func TestWeaponPickupsSpawnAtSpotsUpToTwo(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 3
	cfg.RoundTime = 0 // раунд не кончается за прогон
	w := NewWorld(cfg)
	weapons := func() []Pickup {
		var out []Pickup
		for _, pk := range w.pickups {
			if streamOf(pk.Type) == streamWeapon {
				out = append(out, pk)
			}
		}
		return out
	}
	for _, step := range []struct {
		ticks, want int
	}{{ticksFor(30) - 1, 0}, {1, 1}, {ticksFor(30) - 1, 1}, {1, 2}, {ticksFor(60), 2}} {
		tickN(w, step.ticks)
		if got := len(weapons()); got != step.want {
			t.Fatalf("тик %d: оружия на карте %d, want %d", w.tick, got, step.want)
		}
	}
	for _, pk := range weapons() {
		if _, ok := weaponOf(pk.Type); !ok || !isWeaponSpot(cfg.Map, pk.Pos) {
			t.Errorf("пикап %s в %v: не оружие или не в точке карты", pk.Type, pk.Pos)
		}
	}
	if a, b := weapons()[0].Pos, weapons()[1].Pos; a == b {
		t.Errorf("два пикапа оружия в одной точке %v", a)
	}
}

func isWeaponSpot(m Map, p Vec2) bool {
	for _, s := range m.WeaponSpots {
		if s == p {
			return true
		}
	}
	return false
}

// За много появлений встречаются все три вида оружия.
func TestWeaponPickupTypesVary(t *testing.T) {
	cfg := testConfig()
	cfg.Map.WeaponSpots = []Vec2{{X: 200, Y: 200}}
	cfg.WeaponPickupInterval, cfg.MaxWeaponPickups = dt, 1
	w := NewWorld(cfg)
	seen := map[PickupType]int{}
	for range 300 {
		w.pickups = w.pickups[:0]
		w.Update(dt, nil)
		for _, pk := range w.pickups {
			seen[pk.Type]++
		}
	}
	for _, wp := range weaponPickups {
		if seen[wp.typ] < 50 {
			t.Errorf("%s появился %d раз из 300: %v", wp.typ, seen[wp.typ], seen)
		}
	}
}

// Точка, у которой стоит игрок, пропускается.
func TestWeaponSpotSkipsPlayers(t *testing.T) {
	cfg := testConfig()
	cfg.Map.WeaponSpots = []Vec2{{X: 200, Y: 200}, {X: 800, Y: 800}}
	cfg.WeaponPickupInterval, cfg.MaxWeaponPickups = 1, 2
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 210, 200)
	tickN(w, 2*ticksFor(1))
	if len(w.pickups) != 1 || w.pickups[0].Pos != (Vec2{X: 800, Y: 800}) {
		t.Fatalf("пикапы %+v, want один в (800, 800): у (200, 200) стоит игрок", w.pickups)
	}
}

// Без точек на карте оружие не появляется, и поток не трогает rng: симуляция
// та же, что с выключенным потоком.
func TestWeaponPickupsNeedSpots(t *testing.T) {
	run := func(interval float64) *World {
		cfg := testConfig()
		cfg.PickupInterval, cfg.WeaponPickupInterval, cfg.MaxWeaponPickups = 1, interval, 2
		w := NewWorld(cfg)
		w.AddPlayer(1)
		tickN(w, ticksFor(10))
		return w
	}
	on, off := run(1), run(0)
	if on.countPickups(streamWeapon) != 0 {
		t.Fatalf("оружие на карте без точек: %+v", on.pickups)
	}
	if on.rng.Uint64() != off.rng.Uint64() {
		t.Fatal("поток оружия без точек сдвинул rng")
	}
}

// Новый раунд убирает оружие с карты и заново заводит его таймер.
func TestWeaponPickupsResetOnNewRound(t *testing.T) {
	cfg := DefaultConfig()
	w := NewWorld(cfg)
	dropPickup(w, PickupRifle, 400, 230)
	w.weaponTimer = 1
	w.startRound()
	if n := w.countPickups(streamWeapon); n != 0 || w.weaponTimer != cfg.WeaponPickupInterval {
		t.Fatalf("после нового раунда оружия %d, таймер %.1f; want 0, %.0f", n, w.weaponTimer, cfg.WeaponPickupInterval)
	}
}
