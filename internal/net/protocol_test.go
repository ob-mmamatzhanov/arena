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
