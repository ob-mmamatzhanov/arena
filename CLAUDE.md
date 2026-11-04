# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
go build ./... && go vet ./... && go test ./...      # the standard check before every commit
go test -race ./...                                    # room/net are concurrent — run before pushing
go test ./internal/game -run TestDashImpulse -v        # single test
go test ./internal/game -run '^$' -bench BenchmarkUpdate8Players100Bullets -benchmem
go run ./cmd/server -addr :8080 -origins localhost:5173   # WS at GET /ws, health at GET /healthz
```

**gofmt on Windows:** `core.autocrlf=true`, so working-copy files have CRLF and `gofmt -l` flags every file. Check formatting ignoring `\r`:
`for f in $(git ls-files -m -o --exclude-standard '*.go'); do tr -d '\r' < $f | gofmt -d; done`

`python` on this machine is the Microsoft Store stub — don't use it for scripted edits.

## Architecture

Authoritative real-time arena server (Go, `nhooyr.io/websocket`), 20 Hz ticks, one room `lobby` of up to 8 players.

Three layers with a strict dependency direction `room → game`, `room → net`; **`game` and `net` don't know about each other**:

- **`internal/game`** — pure simulation, no I/O, no goroutines, not thread-safe. `World.Update(dt, inputs)` advances one tick; `World.Snapshot()` is a pure read returning copies. All tunables live in `Config` (`DefaultConfig()` is production). Mechanics are split by file: `world.go` (movement, obstacles, bullets, rounds, heat), `pickups.go`, `dash.go`, `events.go`.
- **`internal/net`** — wire protocol (`protocol.go`: JSON types `Input`, `Welcome`, `Snapshot`, `Event` …) and `Conn`, a WebSocket wrapper where only `WritePump` writes to the socket and everyone else enqueues via non-blocking `TrySend`.
- **`internal/room`** — the only place both meet. A `Room` is **a single goroutine** (`Run`) that owns the world, the clients and the input buffers; the outside talks to it only through the `join`/`leave`/`inputs` channels, so there are no mutexes. The world is mutated only in `tick()`. `broadcast` converts `game.Snapshot` → `net.Snapshot`, marshals once, and drops clients whose send queue is full.

Per connection (`room/handler.go`): accept → `Room.Join` (Welcome is queued before Join returns) → `WritePump` goroutine + blocking `ReadPump` → `Leave`. Joins/leaves are applied at the next tick.

### Adding a field to the protocol

A new piece of state usually touches four places: the `game` type (`Player`/`PlayerState`/`Snapshot`), its `net` counterpart with a JSON tag, the mapping in `room.broadcast` (or `handleJoin` for `Welcome`, `bufferInput` for `Input`), and a JSON-shape test in `room_test.go` / `net/protocol_test.go`. Static data (map obstacles, world size, speed/radius) goes in `Welcome` once, not in snapshots.

## Invariants the code relies on

- **Determinism.** All randomness comes from `World.rng` (seeded by `Config.Seed`; the room seeds from time) — never the global `rand`. Anything that consumes rng or emits events iterates players in `sortedIDs()` order, not map order. Tests depend on this.
- **Zero allocations in `Update`** (the benchmark reports `0 allocs/op`): slices (`bullets`, `pickups`, `events`, `ids`) are reused with `s[:0]` and filtered in place. Hot paths avoid `math.Hypot`/`math.Min`/`math.Max` (not inlined on amd64) — compare squared distances and use the local `clamp`.
- **Config value ≤ 0 disables a mechanic** (`RoundTime`, `KillLimit`, `PickupInterval`, `DashDuration`). `testConfig()` in `world_test.go` keeps rounds and pickup spawning off so unrelated tests aren't affected — enable them per test.
- **Float timers** compare against `timeEps`, contact distances against `contactEps`; exact `==` on accumulated `dt` breaks.
- **Events** (`Snapshot.Events`) are reset at the start of each `Update`, so each event appears in exactly one tick's snapshot. `net.Event.MarshalJSON` writes only the fields of its type (no `omitempty` on numbers: 0 is a valid coordinate/ID).
- **Intermission freezes the world**: inputs are dropped entirely (`LastSeq` doesn't advance); only the phase timer runs. `startRound` resets per-round player state — new per-player state must be reset there too.
- **Input merging**: the room keeps the newest input per player per tick by `seq`, but ORs one-shot/edge flags (`Fire`, `Dash`) across inputs of the same tick so short presses aren't lost.
- Player body radius (`PlayerRadius`, used for walls/borders and sent in Welcome for client prediction) and hitbox (`HitRadius`) are separate config values.

## Conventions

- Code comments, test messages and commit message bodies are in Russian; commit subjects are in English.
- Each mechanic goes in its own commit with its tests; every commit should pass build/vet/test on its own.
- Ticks in tests: `dt = 0.05`; helpers `place`, `tickN`, `fireOnce`, `runCollectHits`, `hitsOf`, `dropPickup` live in the `_test.go` files of `internal/game`.
