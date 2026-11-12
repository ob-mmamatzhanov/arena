# CLAUDE.md

Этот файл содержит указания для Claude Code (claude.ai/code) при работе с кодом в этом репозитории.

## Команды

```sh
go build ./... && go vet ./... && go test ./...      # стандартная проверка перед каждым коммитом
go test -race ./...                                    # room/net конкурентны — прогнать перед push
go test ./internal/game -run TestDashImpulse -v        # отдельный тест
go test ./internal/game -run '^$' -bench BenchmarkUpdate8Players100Bullets -benchmem
go run ./cmd/server -addr :8080 -origins localhost:5173   # WS на GET /ws?room=|?mode=, /rooms, /metrics, /healthz, /leaderboard
                                                          # -ip-header: заголовок прокси с IP клиента для лимита POST /rooms (прод — Fly-Client-IP)
                                                          # -redis (по умолчанию $REDIS_URL): пусто или Redis недоступен — таблица в памяти
go test ./internal/game -run TestPrintMap -v           # ASCII-схема карты: '#' стена, 'x' ящик, 'S' спавн
```

**Push в `master` = деплой в прод.** `.github/workflows/deploy.yml` на каждый push в `master` гоняет `go vet` + `go test -race` и затем `flyctl deploy` (приложение `arena-ob`, `fly.toml`). Флаги продакшена (`-origins`, `-ip-header`) заданы дважды — в `CMD` у `Dockerfile` и в `[processes]` у `fly.toml`; менять нужно оба.

**gofmt на Windows:** включён `core.autocrlf=true`, поэтому в рабочей копии файлы с CRLF и `gofmt -l` помечает вообще все файлы. Проверять форматирование, игнорируя `\r`:
`for f in $(git ls-files -m -o --exclude-standard '*.go'); do tr -d '\r' < $f | gofmt -d; done`

**`-race` на Windows** требует cgo и C-компилятора, иначе сборка падает на `# runtime/cgo: C compiler "gcc" not found`. Поставлен WinLibs MinGW-w64 (`winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT`), в `go env` выставлено `CGO_ENABLED=1`. После установки GoLand нужно перезапустить, чтобы он подхватил новый PATH.

`python` на этой машине — заглушка из Microsoft Store, не использовать её для скриптовых правок. Для разовых расчётов (подбор координат на карте и т. п.) удобен временный `internal/game/zz_tmp_test.go`: из него доступны неэкспортированные `segmentRectT`, `pointRectDist`, `circleFree`; запуск через `-run … -v` и `t.Log`, после — удалить файл.

## Архитектура

Авторитетный сервер real-time арены (Go, `github.com/coder/websocket` — поддерживаемый форк nhooyr.io/websocket), тики 20 Гц, до 50 комнат по 2–8 игроков.

Слои со строгим направлением зависимостей `manager → room`, `room → game`, `room → net`, `room → leaderboard`; **`game`, `net` и `leaderboard` ничего не знают друг о друге** (общая проверка CORS-`Origin` — в листовом `internal/cors`):

- **`internal/game`** — чистая симуляция, без I/O, без горутин, не потокобезопасна. `World.Update(dt, inputs)` продвигает один тик; `World.Snapshot()` — чистое чтение, возвращающее копии. Все настройки живут в `Config` (`DefaultConfig()` — продакшен). Механики разнесены по файлам: `world.go` (движение, препятствия, пули, раунды, перегрев), `pickups.go`, `dash.go`, `events.go`, `maps.go`.
- **`internal/net`** — протокол обмена (`protocol.go`: JSON-типы `Input`, `Welcome`, `Snapshot`, `Event` …) и `Conn`, обёртка над WebSocket, где в сокет пишет только `WritePump`, а все остальные кладут в очередь через неблокирующий `TrySend`.
- **`internal/leaderboard`** — таблица лидеров за всё время: интерфейс `Store` (`AddScore`, `Top`), реализации `Memory` и `Redis` (go-redis/v9: `ZINCRBY lb:scores`, `HSET lb:names`, член ZSET — `playerKey`), `Open(url)` с откатом на `Memory` при пустом URL или недоступном Redis (в лог — только хост) и HTTP-обработчик `GET /leaderboard` с той же проверкой `Origin`, что у WebSocket. Тесты Redis идут на `miniredis` — живой Redis не нужен.
- **`internal/room`** — единственное место, где слои встречаются. `Room` — это **одна горутина** (`Run`), владеющая миром, клиентами и буферами ввода; снаружи с ней общаются только через каналы `join`/`leave`/`inputs`, поэтому мьютексов нет. Мир мутируется только в `tick()`. `broadcast` конвертирует `game.Snapshot` → `net.Snapshot`, маршалит один раз и выбрасывает клиентов, у которых очередь отправки переполнена. Настройки комнаты — опции `New(..., WithMode, WithMaxPlayers, WithGameConfig, WithTickObserver)`. Наружу комната публикует только атомарные `Players()`, `Seats()`, `Phase()` (пишет их горутина `Run`, фазу — лишь при смене).
- **`internal/manager`** — реестр комнат `map[id]*entry` под одним `sync.RWMutex` (единственный мьютекс над комнатами; у лимитера `POST /rooms` свой). `Create`/`Get`/`List`/`ByCode`/`FindOrCreate`/`Shutdown`, фоновый `reap` раз в `ReapInterval` удаляет комнаты, пустые `EmptyTTL` (часы инжектируются через `Config.Now`; тесты зовут `reap()` руками на `newManager` без фоновой горутины). HTTP — `Register(mux, HTTPConfig)`: `/rooms`, `/rooms/by-code/{code}`, `/ws`, `/metrics` (собственный `prometheus.Registry`, не глобальный). Режим → `game.Config` — в `gameConfig(mode)`; `zombie` пока запускает обычную арену.

**Места в комнате** (`Room.seats`) = подключённые + зарезервированные. `Reserve()` берёт место CAS-ом до входа, `JoinReserved` его использует (при ошибке освобождает), уход/дроп игрока освобождает в `removeClient`. `FindOrCreate` возвращает комнату **с уже занятым местом** — вызывающий обязан `JoinReserved` или `Unreserve`; без этого параллельные автозаполнения переполняли бы комнату. `Retire()` CAS-ом 0 → отрицательное списывает пустую комнату: после него `Reserve` → `ErrClosed`, так `reap` не гоняется с входящим игроком. `/ws` резервирует место только после `ReadJoin`, чтобы молчащий клиент не держал его.

Профиль игрока (имя, `playerKey`, индекс цвета — `room/profile.go`) живёт в комнате (`clients map[PlayerID]*client`), а не в `game`: симуляции он не нужен, `broadcast` подставляет имя и цвет в снапшот и `round.scores`. Фраги пишутся в `Store` асинхронно: `tick` → `recordKills` неблокирующе кладёт +1 стрелку в буферизованный канал, единственный писатель — горутина `writeScores`; при полной очереди запись теряется, тик не ждёт. При остановке `Run` дописывает очередь не дольше `flushTimeout`.

На каждое соединение (`manager/http.go` → `room.Play`): разбор `?room=`/`?mode=` → accept (нет комнаты — close `4004`, плохой режим — `4000`) → `Conn.ReadJoin` (первое сообщение обязано быть `join`, таймаут `joinTimeout` 5 с; в тестах `room` подменяется через `SetJoinTimeout` из `export_test.go`) → `Reserve`/`FindOrCreate` → `room.Play`: `JoinReserved` (Welcome кладётся в очередь до возврата) → горутина `WritePump` + блокирующий `ReadPump` → `Leave`. `room.Handler` — тот же путь для одной комнаты, им пользуются интеграционные тесты `room`. Подключения и отключения применяются на следующем тике.

### Карты

`Map` (`maps.go`) встроен в `Config`, поэтому `cfg.Width`, `cfg.Obstacles`, `cfg.Spawns`, `cfg.PickupZones` читаются напрямую; `DefaultMap()` = `ArenaMap()` (800×600). Комнаты менеджера берут карту по вместимости — `MapForPlayers` в `manager.gameConfig`: от `LargeMapMinPlayers` (5) — `ArenaLargeMap()` (арена + укрытия + зоны ammo). Поток ammo масштабируется числом игроков (`AmmoBasePlayers`, `World.ammoStream`). Мир читает только поля `Map`, так что новая карта — это новая функция без правок в `world.go`/`pickups.go`. Пустые `Spawns` / зоны типа — старое поведение (случайная свободная точка). `Obstacle.Kind` (`brick`/`crate`) — только для отрисовки на клиенте, на симуляцию не влияет; пустой `Kind` означает `brick` (`KindOrDefault()`), в `Welcome` поле всегда заполнено.

Принципы карты описаны в комментарии к `ArenaMap` и проверяются в `maps_test.go` для каждой карты из `allMaps()` (новую карту добавлять туда): поворотная симметрия 180° (с учётом `Kind`), препятствия не пересекаются, проходы ≥ 32 px либо вплотную к краю (исключение — щель между двумя выровненными ящиками уже диаметра игрока, но шире пули: линия прострела, не проход), связность свободного пространства, укрытие рядом, спавны скрыты от центра и разнесены, зоны пикапов целиком не ближе `PickupEdgeMargin` к краю. Тесты с `DefaultConfig()`/`DefaultMap()` (например, `killOnce` в `rounds_test.go`) ставят игроков в конкретные координаты — после правки геометрии карты они могут попасть в стену.

### Добавление поля в протокол

Новая часть состояния обычно затрагивает четыре места: тип в `game` (`Player`/`PlayerState`/`Snapshot`), его аналог в `net` с JSON-тегом, маппинг в `room.broadcast` (либо `handleJoin` для `Welcome`, `bufferInput` для `Input`) и тест на форму JSON в `room_test.go` / `net/protocol_test.go`. Статические данные (препятствия карты, размер мира, скорость/радиус) уходят в `Welcome` один раз, а не в снапшоты.

## Инварианты, на которые опирается код

- **Детерминизм.** Вся случайность берётся из `World.rng` (сид из `Config.Seed`; комната сидит от времени) — никогда из глобального `rand`. Всё, что потребляет rng или порождает события, обходит игроков в порядке `sortedIDs()`, а не в порядке map. Тесты на это завязаны.
- **Ноль аллокаций в `Update`** (бенчмарк показывает `0 allocs/op`): слайсы (`bullets`, `pickups`, `events`, `ids`) переиспользуются через `s[:0]` и фильтруются на месте. Горячие пути избегают `math.Hypot`/`math.Min`/`math.Max` (не инлайнятся на amd64) — сравниваются квадраты расстояний и используется локальный `clamp`; встроенные `min`/`max` допустимы.
- **Значение конфига ≤ 0 выключает механику** (`RoundTime`, `KillLimit`, `PickupInterval`, `AmmoPickupInterval`, `PickupMinPickupDist`, `PickupEdgeMargin`, `DashDuration`, `MaxAmmo`, `RespawnDelay`, `InvulnTime`). `testConfig()` в `world_test.go` (карта 1000×1000 без препятствий) держит раунды, спавн пикапов, патроны, задержку респауна, неуязвимость и ограничения расстановки пикапов выключенными, чтобы не задевать несвязанные тесты — включать их нужно отдельно в каждом тесте.
- **Вещественные таймеры** сравниваются с `timeEps`, контактные расстояния — с `contactEps`; точное `==` на накопленном `dt` ломается.
- **События** (`Snapshot.Events`) сбрасываются в начале каждого `Update`, поэтому каждое событие попадает ровно в один снапшот тика. `net.Event.MarshalJSON` пишет только поля своего типа (без `omitempty` на числах: 0 — валидная координата/ID).
- **Перерыв между раундами замораживает мир**: ввод отбрасывается целиком (`LastSeq` не растёт); работает только таймер фазы. `startRound` сбрасывает пораундовое состояние игроков — новое состояние игрока нужно сбрасывать там же.
- **Слияние ввода**: комната оставляет самый свежий ввод игрока за тик по `seq`, но ORит одноразовые/фронтовые флаги (`Fire`, `Dash`) по всем вводам одного тика, чтобы короткие нажатия не терялись.
- Радиус тела игрока (`PlayerRadius`, используется для стен/границ и отправляется в Welcome для клиентского предсказания) и хитбокс (`HitRadius`) — разные значения конфига.

## Соглашения

- Комментарии в коде, сообщения тестов и тела коммит-сообщений — на русском; заголовки коммитов — на английском.
- Каждая механика идёт отдельным коммитом вместе со своими тестами; каждый коммит должен сам по себе проходить build/vet/test.
- В тестах `room` клиент — `&client{conn: newFakeClient(n), profile: profile{…}}`; интеграционные тесты подключаются через `connect`/`connectAs`/`dialJoin`, которые сначала шлют `join`.
- Тики в тестах: `dt = 0.05`; хелперы `place`, `tickN`, `fireOnce`, `killOnce`, `runCollectHits`, `hitsOf`, `dropPickup` лежат в `_test.go` файлах `internal/game`; для карт — `forEachMap`, `freeCells`, `crateSlits` в `maps_test.go`.
- Новое ограничение в тесте стоит проверить на «ловит ли он»: временно откатить механику (или `git stash` файла карты) и убедиться, что тест падает.
