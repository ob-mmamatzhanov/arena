package game

import (
	"math"
	"testing"
)

// deathConfig — testConfig с включёнными задержкой респауна и неуязвимостью
// (значения как в DefaultConfig).
func deathConfig() Config {
	cfg := testConfig()
	cfg.RespawnDelay, cfg.RespawnMinKillerDist, cfg.InvulnTime = 3, 150, 1
	return cfg
}

// newDuel — игроки 1 (стрелок, x=100) и 2 (цель, x=300) без неуязвимости после входа;
// 2 умрёт с одного попадания.
func newDuel(t *testing.T, cfg Config) *World {
	t.Helper()
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	place(w, 2, 300, 500)
	w.players[1].invulnLeft, w.players[2].invulnLeft = 0, 0
	w.players[2].HP = cfg.BulletDamage
	return w
}

// killTarget — игрок 1 стреляет вправо и убивает 2; возвращается, когда в
// снапшоте тика есть событие kill.
func killTarget(t *testing.T, w *World) {
	t.Helper()
	fireOnce(w, 1, 0)
	if countEvents(w.Snapshot().Events, EventKill) == 1 {
		return
	}
	for range 20 {
		w.Update(dt, nil)
		if countEvents(w.Snapshot().Events, EventKill) == 1 {
			return
		}
	}
	t.Fatal("убийства не произошло")
}

func findEvent(events []Event, typ EventType) (Event, bool) {
	for _, e := range events {
		if e.Type == typ {
			return e, true
		}
	}
	return Event{}, false
}

func TestKilledPlayerIsDead(t *testing.T) {
	w := newDuel(t, deathConfig())
	killTarget(t, w)

	s := w.Snapshot()
	if _, ok := findEvent(s.Events, EventRespawn); ok {
		t.Fatal("respawn в тике смерти при включённой задержке")
	}
	got := playerState(t, w, 2)
	if !got.Dead || !approx(got.RespawnIn, 3) || got.HP != 0 || got.Invuln {
		t.Fatalf("убитый = %+v, want dead, respawnIn 3, hp 0, не неуязвим", got)
	}
	if s := playerState(t, w, 1).Score; s != 1 {
		t.Fatalf("счёт убийцы = %d, want 1", s)
	}
}

func TestDeadPlayerDoesNotMoveOrAct(t *testing.T) {
	w := newDuel(t, deathConfig())
	killTarget(t, w)
	before := playerState(t, w, 2)
	bullets := len(w.Snapshot().Bullets)

	for i := uint32(1); i <= 10; i++ {
		w.Update(dt, map[PlayerID]Input{2: {Seq: 100 + i, MoveX: 1, MoveY: 1, Angle: 2, Fire: true, Dash: true}})
	}
	got := playerState(t, w, 2)
	if got.Pos != before.Pos || got.Angle != before.Angle {
		t.Fatalf("мёртвый сдвинулся: %v/%v -> %v/%v", before.Pos, before.Angle, got.Pos, got.Angle)
	}
	if got.LastSeq != before.LastSeq {
		t.Fatalf("LastSeq мёртвого вырос: %d -> %d (ввод должен игнорироваться)", before.LastSeq, got.LastSeq)
	}
	for _, b := range w.Snapshot().Bullets {
		if b.Owner == 2 {
			t.Fatalf("мёртвый выстрелил: %+v (пуль до: %d)", b, bullets)
		}
	}
	if !approx(got.RespawnIn, 3-10*dt) {
		t.Fatalf("respawnIn = %v, want %v", got.RespawnIn, 3-10*dt)
	}
}

func TestBulletsPassThroughDeadPlayer(t *testing.T) {
	w := newDuel(t, deathConfig())
	killTarget(t, w)
	w.players[1].cooldown = 0

	// Вторая пуля летит сквозь тело 2 (x=300) дальше; урона и попаданий нет.
	seq := w.players[1].LastSeq
	w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 1, Angle: 0, Fire: true}})
	w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 2, Angle: 0}})
	for range 10 { // всего 11 тиков полёта — пуля пролетает 275 и минует x=300
		w.Update(dt, nil)
		s := w.Snapshot()
		if len(hitsOf(s.Events)) != 0 {
			t.Fatalf("попадание в мёртвого: %+v", s.Events)
		}
		if _, ok := findEvent(s.Events, EventBulletHit); ok {
			t.Fatalf("пуля остановилась о мёртвого: %+v", s.Events)
		}
	}
	bs := w.Snapshot().Bullets
	if len(bs) != 1 || bs[0].Pos.X <= 300 {
		t.Fatalf("пули = %+v, want одна пуля за телом (x > 300)", bs)
	}
	if got := playerState(t, w, 2); got.HP != 0 || !got.Dead {
		t.Fatalf("мёртвый = %+v, want hp 0 и dead", got)
	}
}

func TestRespawnExactlyAfter3s(t *testing.T) {
	cfg := deathConfig()
	w := newDuel(t, cfg)
	killTarget(t, w)

	tickN(w, 59)
	if got := playerState(t, w, 2); !got.Dead || !approx(got.RespawnIn, dt) {
		t.Fatalf("через 2.95 с: %+v, want ещё мёртв, respawnIn %v", got, dt)
	}
	tickN(w, 1) // ровно 3 с = 60 тиков
	got := playerState(t, w, 2)
	if got.Dead || got.RespawnIn != 0 || got.HP != StartHP || !got.Invuln {
		t.Fatalf("через 3 с: %+v, want жив, respawnIn 0, hp %d, неуязвим", got, StartHP)
	}
	ev, ok := findEvent(w.Snapshot().Events, EventRespawn)
	if !ok || ev.Player != 2 || ev.Pos != got.Pos {
		t.Fatalf("событие respawn = %+v (есть=%v), want игрок 2 в %v", ev, ok, got.Pos)
	}
	if !w.isFree(got.Pos) {
		t.Fatalf("респаун в препятствии: %v", got.Pos)
	}
	// Событие — ровно в одном тике.
	w.Update(dt, nil)
	if _, ok := findEvent(w.Snapshot().Events, EventRespawn); ok {
		t.Fatal("respawn повторился в следующем тике")
	}
}

func TestRespawnAcceptsInputInSameTick(t *testing.T) {
	w := newDuel(t, deathConfig())
	killTarget(t, w)
	tickN(w, 59)
	seq := w.players[2].LastSeq + 1
	w.Update(dt, map[PlayerID]Input{2: {Seq: seq, MoveX: 1}})
	if got := playerState(t, w, 2); got.Dead || got.LastSeq != seq {
		t.Fatalf("тик респауна: %+v, want жив и LastSeq %d", got, seq)
	}
}

func TestRespawnNotNearKiller(t *testing.T) {
	for seed := uint64(1); seed <= 50; seed++ {
		cfg := deathConfig()
		cfg.Seed = seed
		w := newDuel(t, cfg)
		killTarget(t, w)
		place(w, 1, 500, 300) // убийца перешёл — расстояние считается от его позиции в момент респауна
		tickN(w, 60)
		p := playerState(t, w, 2)
		if p.Dead {
			t.Fatalf("seed %d: не возродился", seed)
		}
		if d := math.Hypot(p.Pos.X-500, p.Pos.Y-300); d < cfg.RespawnMinKillerDist {
			t.Fatalf("seed %d: респаун в %v, всего %.1f от убийцы", seed, p.Pos, d)
		}
	}
}

// С большим минимальным расстоянием ограничение действительно отсекает точки:
// без него примерно половина случайных точек оказалась бы ближе.
func TestRespawnPointKeepsLargeKillerDistance(t *testing.T) {
	cfg := deathConfig()
	cfg.RespawnMinKillerDist = 400
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 500, 500)
	for i := range 200 {
		p := w.respawnPoint(2, 1)
		if d := math.Hypot(p.X-500, p.Y-500); d < 400 {
			t.Fatalf("точка %d: %v всего в %.1f от убийцы", i, p, d)
		}
		if !w.isFree(p) {
			t.Fatalf("точка %d: %v в препятствии", i, p)
		}
	}
}

func TestInvulnerableTakesNoDamageButCanFire(t *testing.T) {
	w := newDuel(t, deathConfig())
	w.players[2].HP = StartHP
	w.players[2].invulnLeft = 1

	// 1 стреляет в 2, 2 одновременно стреляет в 1.
	w.Update(dt, map[PlayerID]Input{
		1: {Seq: 1, Angle: 0, Fire: true},
		2: {Seq: 1, Angle: math.Pi, Fire: true},
	})
	shooters := map[PlayerID]bool{}
	for _, b := range w.Snapshot().Bullets {
		shooters[b.Owner] = true
	}
	if !shooters[2] {
		t.Fatal("неуязвимый не смог выстрелить")
	}
	w.Update(dt, map[PlayerID]Input{1: {Seq: 2}, 2: {Seq: 2}})

	absorbed := false
	for range 10 {
		w.Update(dt, nil)
		evs := w.Snapshot().Events
		for _, h := range hitsOf(evs) {
			if h.Target == 2 {
				t.Fatalf("попадание по неуязвимому: %+v", h)
			}
		}
		if e, ok := findEvent(evs, EventBulletHit); ok && math.Abs(e.Pos.X-300) < 20 {
			absorbed = true
		}
	}
	if !absorbed {
		t.Fatal("пуля не остановилась о неуязвимого (нет bulletHit у цели)")
	}
	if hp := playerState(t, w, 2).HP; hp != StartHP {
		t.Fatalf("hp неуязвимого = %d, want %d", hp, StartHP)
	}
	if hp := playerState(t, w, 1).HP; hp != StartHP-w.cfg.BulletDamage {
		t.Fatalf("пуля неуязвимого не нанесла урон: hp 1 = %d", hp)
	}
}

func TestInvulnerabilityLasts1s(t *testing.T) {
	w := NewWorld(deathConfig())
	w.AddPlayer(1) // подключившийся появляется с неуязвимостью
	if !playerState(t, w, 1).Invuln {
		t.Fatal("подключившийся не неуязвим")
	}
	tickN(w, 19)
	if !playerState(t, w, 1).Invuln {
		t.Fatal("неуязвимость кончилась раньше 1 с")
	}
	tickN(w, 1)
	if playerState(t, w, 1).Invuln {
		t.Fatal("неуязвимость длится дольше 1 с")
	}
}

// Попадание в последнем тике неуязвимости гасится, в следующем — наносит урон.
func TestInvulnerabilityBoundaryAfterRespawn(t *testing.T) {
	w := newDuel(t, deathConfig())
	killTarget(t, w)
	// Респаун в тике R, неуязвимость гасит попадания тиков R … R+19.
	// Между тиками R+18 и R+19 остаётся 0.05 с — попадание ещё гасится.
	tickN(w, 60)
	tickN(w, 18)
	p := w.players[2]
	if !p.invulnerable() {
		t.Fatal("перед 20-м тиком после респауна уже уязвим")
	}
	at := p.Pos
	w.applyHit(1, p, at)
	if p.HP != StartHP {
		t.Fatalf("урон в последнем тике неуязвимости: hp %d", p.HP)
	}
	tickN(w, 1)
	w.applyHit(1, p, at)
	if p.HP != StartHP-w.cfg.BulletDamage {
		t.Fatalf("после неуязвимости hp = %d, want %d", p.HP, StartHP-w.cfg.BulletDamage)
	}
}

func TestNewRoundRevivesDead(t *testing.T) {
	cfg := deathConfig()
	cfg.KillLimit, cfg.IntermissionTime = 1, 1
	w := newDuel(t, cfg)
	killTarget(t, w) // фраг достигает лимита — раунд кончается, 2 мёртв
	if w.phase != PhaseIntermission || !w.players[2].dead() {
		t.Fatalf("setup: phase %q, dead %v", w.phase, w.players[2].dead())
	}
	tickN(w, 20)
	got := playerState(t, w, 2)
	if w.phase != PhasePlay || got.Dead || got.HP != StartHP || !got.Invuln {
		t.Fatalf("новый раунд: phase %q, игрок %+v, want жив, hp %d, неуязвим", w.phase, got, StartHP)
	}
}
