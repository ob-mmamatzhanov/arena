package net

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventJSONShapes(t *testing.T) {
	tests := []struct {
		ev   Event
		want string
	}{
		{Event{Type: EventHit, X: 1.5, Y: 0, TargetID: 2, ShooterID: 1, PlayerID: 9, PickupType: "x"},
			`{"type":"hit","x":1.5,"y":0,"targetId":2,"shooterId":1}`},
		{Event{Type: EventKill, X: 5, Y: 5, TargetID: 2, ShooterID: 1},
			`{"type":"kill","targetId":2,"shooterId":1}`},
		{Event{Type: EventShot, PlayerID: 3, X: 0, Y: 10, TargetID: 7},
			`{"type":"shot","playerId":3,"x":0,"y":10}`},
		{Event{Type: EventBulletHit, X: 1000, Y: 0, ShooterID: 4},
			`{"type":"bulletHit","x":1000,"y":0}`},
		{Event{Type: EventPickup, PlayerID: 3, PickupType: PickupHealth, X: 1},
			`{"type":"pickup","playerId":3,"pickupType":"health"}`},
		{Event{Type: EventRespawn, PlayerID: 0, X: 0, Y: 7, TargetID: 2, PickupType: "x"},
			`{"type":"respawn","playerId":0,"x":0,"y":7}`},
		// Индекс препятствия 0 и hp 0 законны — поля есть всегда.
		{Event{Type: EventCrateHit, Obstacle: 0, HP: 0, X: 300, Y: 106, PlayerID: 4},
			`{"type":"crateHit","obstacle":0,"hp":0,"x":300,"y":106}`},
		{Event{Type: EventCrateHit, Obstacle: 5, HP: 3, X: 1, Y: 2},
			`{"type":"crateHit","obstacle":5,"hp":3,"x":1,"y":2}`},
		{Event{Type: EventCrateBroken, Obstacle: 0, HP: 9, X: 316, Y: 106},
			`{"type":"crateBroken","obstacle":0,"x":316,"y":106}`},
		// zombieId и shooterId 0 законны — поля есть всегда.
		{Event{Type: EventZombieKill, ZombieID: 0, ShooterID: 0, X: 12, Y: 0, TargetID: 3, HP: 5},
			`{"type":"zombieKill","zombieId":0,"shooterId":0,"x":12,"y":0}`},
		{Event{Type: EventZombieKill, ZombieID: 7, ShooterID: 2, X: 1, Y: 2},
			`{"type":"zombieKill","zombieId":7,"shooterId":2,"x":1,"y":2}`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.ev)
		if err != nil {
			t.Fatalf("%s: %v", tt.ev.Type, err)
		}
		if string(got) != tt.want {
			t.Errorf("%s:\n got  %s\n want %s", tt.ev.Type, got, tt.want)
		}
		// Разбор обратно сохраняет значимые поля.
		var back Event
		if err := json.Unmarshal(got, &back); err != nil || back.Type != tt.ev.Type {
			t.Errorf("%s: round-trip = %+v, %v", tt.ev.Type, back, err)
		}
	}
}

func TestEventUnknownTypeFails(t *testing.T) {
	if _, err := json.Marshal(Event{Type: "boom"}); err == nil {
		t.Fatal("unknown event type must fail to marshal")
	}
}

func TestSnapshotOmitsEmptyEvents(t *testing.T) {
	for _, evs := range [][]Event{nil, {}} {
		b, err := json.Marshal(Snapshot{Type: TypeSnapshot, Events: evs})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"events"`) {
			t.Fatalf("empty events serialized: %s", b)
		}
	}
	b, _ := json.Marshal(Snapshot{Events: []Event{{Type: EventBulletHit, X: 1, Y: 2}}})
	if !strings.Contains(string(b), `"events":[{"type":"bulletHit","x":1,"y":2}]`) {
		t.Fatalf("events not serialized: %s", b)
	}
	if strings.Contains(string(b), `"hits"`) {
		t.Fatalf("legacy hits field still present: %s", b)
	}
}

// crates опускается, пока все ящики целые, и сериализуется {i, hp} с нулями.
func TestSnapshotCrates(t *testing.T) {
	for _, cs := range [][]Crate{nil, {}} {
		b, err := json.Marshal(Snapshot{Type: TypeSnapshot, Crates: cs})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"crates"`) {
			t.Fatalf("пустой crates сериализован: %s", b)
		}
	}
	b, _ := json.Marshal(Snapshot{Crates: []Crate{{I: 0, HP: 0}, {I: 4, HP: 2}}})
	if !strings.Contains(string(b), `"crates":[{"i":0,"hp":0},{"i":4,"hp":2}]`) {
		t.Fatalf("crates: %s", b)
	}
}

// crateHits в welcome есть всегда, и с нулём (ящики неразрушимы).
func TestWelcomeCrateHits(t *testing.T) {
	for n, want := range map[int]string{0: `"crateHits":0`, 5: `"crateHits":5`} {
		b, _ := json.Marshal(Welcome{Type: TypeWelcome, CrateHits: n})
		if !strings.Contains(string(b), want) {
			t.Errorf("CrateHits=%d: %s", n, b)
		}
	}
}

// Поля режима zombie: zombies опускается без зомби, wave/zombiesLeft/kills
// есть с нулями только там, где заданы, zombieRadius — только в zombie-комнате.
func TestZombieFields(t *testing.T) {
	b, _ := json.Marshal(Snapshot{Type: TypeSnapshot, Zombies: []Zombie{}, Round: Round{Phase: PhasePlay}})
	if s := string(b); strings.Contains(s, `"zombies"`) || strings.Contains(s, `"wave"`) ||
		strings.Contains(s, `"zombiesLeft"`) || strings.Contains(s, `"kills"`) {
		t.Fatalf("поля зомби в снапшоте без них: %s", b)
	}

	zero := 0
	b, _ = json.Marshal(Snapshot{
		Zombies: []Zombie{{ID: 0, X: 0, Y: 5, HP: 30, Speed: 90}},
		Round:   Round{Phase: PhasePlay, TimeLeft: 0, Wave: &zero, ZombiesLeft: &zero},
	})
	for _, want := range []string{
		`"zombies":[{"id":0,"x":0,"y":5,"hp":30,"speed":90}]`,
		`"round":{"phase":"play","timeLeft":0,"wave":0,"zombiesLeft":0}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("нет %s в %s", want, b)
		}
	}

	wave, kills := 3, 0
	b, _ = json.Marshal(Round{Phase: PhaseGameOver, TimeLeft: 8, Wave: &wave, Kills: &kills})
	if string(b) != `{"phase":"gameover","timeLeft":8,"wave":3,"kills":0}` {
		t.Fatalf("gameover: %s", b)
	}

	if b, _ := json.Marshal(Welcome{Type: TypeWelcome}); strings.Contains(string(b), "zombieRadius") {
		t.Fatalf("zombieRadius в арене: %s", b)
	}
	if b, _ := json.Marshal(Welcome{ZombieRadius: 12}); !strings.Contains(string(b), `"zombieRadius":12`) {
		t.Fatalf("zombieRadius: %s", b)
	}
}
