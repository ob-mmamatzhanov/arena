package game

import "testing"

// fireWorld — один стрелок на пустом поле, перезарядка 0.2 с, без патронов
// (MaxAmmo = 0 — стрельба не ограничена), FireHoldTime задан явно.
func fireWorld(hold float64) *World {
	cfg := testConfig()
	cfg.FireHoldTime = hold
	w := NewWorld(cfg)
	w.AddPlayer(1)
	place(w, 1, 500, 500)
	return w
}

// shotsDuring прогоняет n тиков; send(i) решает, приходит ли в тике i ввод
// (и с каким Fire). Возвращает число выстрелов.
func shotsDuring(w *World, n int, send func(i int) (Input, bool)) int {
	shots, seq := 0, uint32(0)
	for i := range n {
		var inputs map[PlayerID]Input
		if in, ok := send(i); ok {
			seq++
			in.Seq = seq
			inputs = map[PlayerID]Input{1: in}
		}
		w.Update(dt, inputs)
		for _, e := range w.events {
			if e.Type == EventShot {
				shots++
			}
		}
	}
	return shots
}

// Клик (один ввод с Fire), после которого ввод замолк на секунду — лаг или
// фоновая вкладка: ровно один выстрел, а не по выстрелу на каждую перезарядку.
func TestFireExpiresWithoutFreshInput(t *testing.T) {
	w := fireWorld(DefaultConfig().FireHoldTime)
	got := shotsDuring(w, 20, func(i int) (Input, bool) { return Input{Fire: true}, i == 0 })
	if got != 1 {
		t.Fatalf("один ввод с fire и 1 с тишины: выстрелов %d, want 1", got)
	}
}

// Удержание огня с вводом каждый тик — очередь в темпе FireCooldown (0.2 с):
// за 1 с — 5 выстрелов.
func TestHeldFireKeepsFiring(t *testing.T) {
	w := fireWorld(DefaultConfig().FireHoldTime)
	got := shotsDuring(w, 20, func(int) (Input, bool) { return Input{Fire: true}, true })
	if got != 5 {
		t.Fatalf("удержание 1 с: выстрелов %d, want 5", got)
	}
}

// Джиттер: ввод приходит через тик (каждый второй пропал) — удержание не
// прерывается, темп тот же.
func TestHeldFireSurvivesOneMissedTick(t *testing.T) {
	w := fireWorld(DefaultConfig().FireHoldTime)
	got := shotsDuring(w, 20, func(i int) (Input, bool) { return Input{Fire: true}, i%2 == 0 })
	if got != 5 {
		t.Fatalf("удержание с пропуском каждого второго тика: выстрелов %d, want 5", got)
	}
}

// Отпускание огня — новый ввод без Fire — гасит его сразу, независимо от срока.
func TestFireReleasedByInput(t *testing.T) {
	w := fireWorld(DefaultConfig().FireHoldTime)
	got := shotsDuring(w, 20, func(i int) (Input, bool) { return Input{Fire: i < 5}, i <= 5 })
	if got != 2 { // тики 0 и 4 (перезарядка 4 тика), в тике 5 огонь отпущен
		t.Fatalf("огонь 5 тиков, затем отпущен: выстрелов %d, want 2", got)
	}
}

// FireHoldTime ≤ 0 — прежнее поведение: Fire держится до следующего ввода.
func TestFireHoldDisabled(t *testing.T) {
	w := fireWorld(0)
	got := shotsDuring(w, 20, func(i int) (Input, bool) { return Input{Fire: true}, i == 0 })
	if got != 5 {
		t.Fatalf("FireHoldTime = 0, один ввод: выстрелов %d, want 5 (как раньше)", got)
	}
}

// Срок удержания короче перезарядки — иначе клик мог бы дать второй выстрел.
func TestDefaultFireHoldShorterThanCooldown(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.FireHoldTime <= 0 || cfg.FireHoldTime >= cfg.FireCooldown {
		t.Fatalf("FireHoldTime %.2f должен быть в (0, FireCooldown %.2f)", cfg.FireHoldTime, cfg.FireCooldown)
	}
}
