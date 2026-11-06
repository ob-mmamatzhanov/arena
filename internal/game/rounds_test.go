package game

import "testing"

func roundConfig(roundTime float64, killLimit int) Config {
	cfg := testConfig()
	cfg.RoundTime, cfg.KillLimit, cfg.IntermissionTime = roundTime, killLimit, 1
	return cfg
}

// tickN крутит мир n тиков без ввода.
func tickN(w *World, n int) {
	for range n {
		w.Update(dt, nil)
	}
}

// killOnce: игрок 1 убивает игрока 2 одним выстрелом (2 стоит в 200 правее).
func killOnce(t *testing.T, w *World) {
	t.Helper()
	// Линия y = 500, x 100…220 свободна и на карте по умолчанию (DefaultMap).
	place(w, 1, 100, 500)
	place(w, 2, 220, 500)
	w.players[2].HP = w.cfg.BulletDamage
	seq, score := w.players[1].LastSeq, w.players[1].Score
	w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 1, Fire: true}})
	w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 2}})
	for range 20 {
		if w.phase == PhaseIntermission || w.players[1].Score > score {
			return
		}
		w.Update(dt, nil)
	}
	t.Fatal("kill did not happen")
}

func TestPlayPhaseCountsDown(t *testing.T) {
	w := NewWorld(roundConfig(10, 0))
	w.AddPlayer(1)
	tickN(w, 20) // 1 с
	r := w.Snapshot().Round
	if r.Phase != PhasePlay || !approx(r.TimeLeft, 9) {
		t.Fatalf("round = %+v, want play with 9 s left", r)
	}
	if r.WinnerID != 0 || r.Scores != nil {
		t.Fatalf("play phase must not carry winner/scores: %+v", r)
	}
}

func TestRoundEndsByTime(t *testing.T) {
	w := NewWorld(roundConfig(1, 0))
	w.AddPlayer(1)
	w.AddPlayer(2)
	tickN(w, 19)
	if p := w.Snapshot().Round.Phase; p != PhasePlay {
		t.Fatalf("phase after 0.95 s = %q, want play", p)
	}
	tickN(w, 1)
	r := w.Snapshot().Round
	if r.Phase != PhaseIntermission || !approx(r.TimeLeft, w.cfg.IntermissionTime) {
		t.Fatalf("round after 1 s = %+v, want intermission with %v s", r, w.cfg.IntermissionTime)
	}
	if r.WinnerID != 0 {
		t.Fatalf("no frags — no winner, got %d", r.WinnerID)
	}
	if len(r.Scores) != 2 || r.Scores[0].ID != 1 || r.Scores[1].ID != 2 {
		t.Fatalf("scores = %+v, want both players sorted by id", r.Scores)
	}
}

func TestRoundEndsByKillLimit(t *testing.T) {
	w := NewWorld(roundConfig(120, 2))
	w.AddPlayer(1)
	w.AddPlayer(2)

	killOnce(t, w)
	if p := w.Snapshot().Round.Phase; p != PhasePlay {
		t.Fatalf("phase after 1 of 2 kills = %q, want play", p)
	}
	tickN(w, 4) // кулдаун успевает истечь
	killOnce(t, w)

	r := w.Snapshot().Round
	if r.Phase != PhaseIntermission {
		t.Fatalf("phase after reaching kill limit = %q, want intermission", r.Phase)
	}
	if r.WinnerID != 1 {
		t.Fatalf("winner = %d, want 1", r.WinnerID)
	}
	want := []ScoreEntry{{ID: 1, Score: 2}, {ID: 2, Score: 0}}
	if len(r.Scores) != 2 || r.Scores[0] != want[0] || r.Scores[1] != want[1] {
		t.Fatalf("scores = %+v, want %+v", r.Scores, want)
	}
}

func TestTieHasNoWinner(t *testing.T) {
	w := NewWorld(roundConfig(1, 0))
	w.AddPlayer(1)
	w.AddPlayer(2)
	w.players[1].Score, w.players[2].Score = 3, 3
	tickN(w, 20)
	if r := w.Snapshot().Round; r.Phase != PhaseIntermission || r.WinnerID != 0 {
		t.Fatalf("round = %+v, want intermission without winner", r)
	}
}

func TestIntermissionFreezesWorld(t *testing.T) {
	w := NewWorld(roundConfig(1, 0))
	w.AddPlayer(1)
	w.AddPlayer(2)
	place(w, 1, 100, 500)
	// Летящая пуля и движущийся игрок к моменту конца раунда.
	w.Update(dt, map[PlayerID]Input{1: {Seq: 1, MoveX: 1, Fire: true}})
	tickN(w, 19)
	before := w.Snapshot()
	if before.Round.Phase != PhaseIntermission || len(before.Bullets) == 0 {
		t.Fatalf("setup: round = %+v, bullets = %d", before.Round, len(before.Bullets))
	}

	// Вводы во время паузы игнорируются: ни движения, ни выстрелов, ни LastSeq.
	for i := uint32(2); i <= 10; i++ {
		w.Update(dt, map[PlayerID]Input{
			1: {Seq: i, MoveX: -1, MoveY: 1, Angle: 2, Fire: true},
			2: {Seq: i, MoveY: -1},
		})
	}
	after := w.Snapshot()
	for i := range before.Players {
		if after.Players[i] != before.Players[i] {
			t.Fatalf("player changed during intermission: %+v -> %+v", before.Players[i], after.Players[i])
		}
	}
	if len(after.Bullets) != len(before.Bullets) || after.Bullets[0] != before.Bullets[0] {
		t.Fatalf("bullets changed during intermission: %+v -> %+v", before.Bullets, after.Bullets)
	}
	if !approx(after.Round.TimeLeft, before.Round.TimeLeft-9*dt) {
		t.Fatalf("intermission timer = %v, want %v", after.Round.TimeLeft, before.Round.TimeLeft-9*dt)
	}
}

func TestRestartResetsRound(t *testing.T) {
	cfg := roundConfig(120, 1)
	cfg.Map = DefaultMap() // препятствия и спавны: рестарт не ставит игроков в стены
	w := NewWorld(cfg)
	w.AddPlayer(1)
	w.AddPlayer(2)
	w.players[1].HP = 50
	killOnce(t, w)
	if r := w.Snapshot().Round; r.Phase != PhaseIntermission || r.WinnerID != 1 {
		t.Fatalf("setup: round = %+v", r)
	}
	// Пуля, выпущенная в последний тик, ещё летит.
	w.bullets = append(w.bullets, Bullet{ID: 999, Owner: 1, Pos: Vec2{X: 50, Y: 50}, ttl: 1})

	tickN(w, 20) // IntermissionTime = 1 с

	s := w.Snapshot()
	if s.Round.Phase != PhasePlay || !approx(s.Round.TimeLeft, cfg.RoundTime) {
		t.Fatalf("round after intermission = %+v, want fresh play", s.Round)
	}
	if len(s.Bullets) != 0 {
		t.Fatalf("bullets not cleared on restart: %+v", s.Bullets)
	}
	for _, p := range s.Players {
		if p.Score != 0 || p.HP != StartHP {
			t.Fatalf("player %d after restart: score=%d hp=%d, want 0/%d", p.ID, p.Score, p.HP, StartHP)
		}
		if !w.isFree(p.Pos) {
			t.Fatalf("player %d respawned inside obstacle at %v", p.ID, p.Pos)
		}
	}
	// Первый ввод после паузы снова применяется.
	seq := w.players[1].LastSeq
	w.Update(dt, map[PlayerID]Input{1: {Seq: seq + 1, Fire: true}})
	if len(w.Snapshot().Bullets) != 1 {
		t.Fatal("input after restart is not applied")
	}
}

func TestPlayerJoiningMidRoundEntersCurrentRound(t *testing.T) {
	w := NewWorld(roundConfig(10, 0))
	w.AddPlayer(1)
	w.players[1].Score = 3
	tickN(w, 40) // 2 с

	w.AddPlayer(2)
	tickN(w, 1)
	s := w.Snapshot()
	if s.Round.Phase != PhasePlay || !approx(s.Round.TimeLeft, 10-41*dt) {
		t.Fatalf("round restarted on join: %+v", s.Round)
	}
	if p1, p2 := playerState(t, w, 1), playerState(t, w, 2); p1.Score != 3 || p2.Score != 0 || p2.HP != StartHP {
		t.Fatalf("join changed scores: p1=%+v p2=%+v", p1, p2)
	}
}
