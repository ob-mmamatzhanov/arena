package game

import (
	"math"
	"testing"
)

// BenchmarkUpdate8Players100Bullets — один тик мира с 8 игроками (все держат огонь)
// и 100 летящими пулями. Перед каждой итерацией пули восстанавливаются из шаблона
// копированием (без аллокаций), чтобы их число не уплывало из-за попаданий/вылетов.
// Варианты obstacles=0 и obstacles=6 показывают цену коллизий с картой в одной сборке.
func BenchmarkUpdate8Players100Bullets(b *testing.B) {
	for _, bc := range []struct {
		name      string
		obstacles []Obstacle
	}{
		{"obstacles=0", nil},
		{"obstacles=default", DefaultMap().Obstacles},
	} {
		b.Run(bc.name, func(b *testing.B) {
			cfg := DefaultConfig()
			cfg.Obstacles = bc.obstacles
			// Без раундов: иначе за сотни тысяч итераций мир уходил бы в
			// intermission, где Update почти ничего не делает, и цифры врали бы.
			cfg.RoundTime, cfg.KillLimit = 0, 0
			// Мгновенный респаун и без неуязвимости: мёртвые и неуязвимые
			// пропускают часть работы, и цифры зависели бы от числа смертей.
			cfg.RespawnDelay, cfg.InvulnTime = 0, 0
			benchUpdate(b, cfg)
		})
	}
}

func benchUpdate(b *testing.B, cfg Config) {
	w := NewWorld(cfg)
	for id := PlayerID(1); id <= 8; id++ {
		w.AddPlayer(id)
	}

	template := make([]Bullet, 100)
	for i := range template {
		a := 2 * math.Pi * float64(i) / float64(len(template))
		template[i] = Bullet{
			ID:    BulletID(i + 1),
			Owner: PlayerID(i%8 + 1),
			Pos:   Vec2{X: 500 + 150*math.Cos(a), Y: 500 + 150*math.Sin(a)},
			Vel:   Vec2{X: cfg.BulletSpeed * math.Cos(a), Y: cfg.BulletSpeed * math.Sin(a)},
			ttl:   cfg.BulletLifetime,
		}
	}
	w.bullets = make([]Bullet, 0, len(template)+8)
	inputs := make(map[PlayerID]Input, 8)

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		w.bullets = append(w.bullets[:0], template...)
		for id := PlayerID(1); id <= 8; id++ {
			w.players[id].Ammo = cfg.MaxAmmo // патроны не кончаются: все держат огонь
			// Направление меняется, чтобы игроки не залипали в одном углу.
			a := float64(i/40) + float64(id)
			inputs[id] = Input{Seq: uint32(i + 1), MoveX: math.Cos(a), MoveY: math.Sin(a), Angle: float64(id), Fire: true}
		}
		w.Update(1.0/20, inputs)
	}
}
