// Package net описывает сетевой протокол и обёртку над WebSocket-соединением.
// Пакет не зависит от игровой логики: преобразование типов делает room.
package net

import (
	"encoding/json"
	"fmt"
)

// Типы исходящих сообщений (поле "type").
const (
	TypeWelcome  = "welcome"
	TypeSnapshot = "snapshot"
)

// TypeJoin — тип первого сообщения клиент → сервер.
const TypeJoin = "join"

// Join — первое сообщение клиента после подключения, до любого Input. Если
// его нет в течение таймаута (5 с) или первым пришло другое сообщение,
// соединение закрывается.
//
// Name — отображаемое имя: 2–16 символов без управляющих, пробелы по краям
// обрезаются; некорректное заменяется на Player_<id>. PlayerKey — постоянный
// идентификатор игрока в таблице лидеров (клиент хранит его у себя), до 64
// символов; пустой или некорректный сервер генерирует и возвращает в Welcome.
type Join struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	PlayerKey string `json:"playerKey"`
}

// Input — сообщение клиент → сервер. Клиент шлёт его каждый свой кадр/тик
// (в том числе без изменений — это одновременно heartbeat).
type Input struct {
	Seq   uint32  `json:"seq"`
	MoveX float64 `json:"moveX"`
	MoveY float64 `json:"moveY"`
	Angle float64 `json:"angle"` // направление взгляда, радианы
	Fire  bool    `json:"fire"`  // огонь зажат
	Dash  bool    `json:"dash"`  // рывок (срабатывает, если кулдаун готов)
}

// PlayerInput — Input, привязанный сервером к конкретному игроку.
// PlayerID проставляет сервер, клиенту ему доверять нельзя.
type PlayerInput struct {
	PlayerID uint32
	Input    Input
}

// Welcome — первое сообщение сервер → клиент после входа в комнату.
type Welcome struct {
	Type     string `json:"type"`
	PlayerID uint32 `json:"playerId"`
	Room     string `json:"room"`
	TickRate int    `json:"tickRate"`
	// Профиль игрока после проверки сервером: итоговое имя, индекс цвета и
	// ключ (сгенерированный, если клиент не прислал корректный) — клиенту
	// стоит сохранить ключ для следующих подключений.
	Name      string `json:"name"`
	Color     int    `json:"color"`
	PlayerKey string `json:"playerKey"`
	// Размеры мира — те же, что использует серверная симуляция.
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	// Параметры движения — нужны клиенту для client-side prediction,
	// чтобы его симуляция совпадала с серверной.
	PlayerSpeed  float64 `json:"speed"` // единиц в секунду
	PlayerRadius float64 `json:"radius"`
	// Obstacles статичны, поэтому отдаются один раз здесь и не дублируются
	// в снапшотах. Всегда массив (не null).
	Obstacles []Obstacle `json:"obstacles"`
}

// Obstacle — препятствие карты: левый верхний угол (x, y), ширина w, высота h,
// вид kind ("brick" или "crate"; всегда заполнен).
type Obstacle struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	Kind string  `json:"kind"`
}

// PlayerState — состояние одного игрока в снапшоте.
type PlayerState struct {
	ID      uint32  `json:"id"`
	Name    string  `json:"name"`
	Color   int     `json:"color"` // индекс в палитре из 8 цветов, у занятых — разные
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Angle   float64 `json:"angle"`
	Heat    float64 `json:"heat"` // перегрев [0, 1]: разброс ±heat×0.25 рад, для прицела
	HP      int     `json:"hp"`
	Score   int     `json:"score"`
	Ammo    int     `json:"ammo"`
	Boosted bool    `json:"boosted"` // действует ускорение от пикапа speed
	DashCd  float64 `json:"dashCd"`  // секунд до готовности рывка, 0 — готов
	LastSeq uint32  `json:"lastSeq"`
	// Мёртвый игрок ждёт респауна: не цель, не двигается, ввод игнорируется.
	Dead      bool    `json:"dead"`
	RespawnIn float64 `json:"respawnIn"` // секунд до респауна, 0 у живого
	Invuln    bool    `json:"invuln"`    // неуязвим после появления, но может стрелять
}

// BulletState — летящая пуля. Скорость передаётся, чтобы клиент мог
// плавно экстраполировать позицию между снапшотами.
type BulletState struct {
	ID    uint32  `json:"id"`
	Owner uint32  `json:"owner"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	VX    float64 `json:"vx"`
	VY    float64 `json:"vy"`
}

// Типы событий (поле event.type).
const (
	EventHit       = "hit"
	EventKill      = "kill"
	EventShot      = "shot"
	EventBulletHit = "bulletHit"
	EventPickup    = "pickup"
	EventRespawn   = "respawn"
)

// Event — одномоментное событие тика этого снапшота. У каждого типа свой
// набор полей (MarshalJSON пишет только их):
//
//	hit:       {type, x, y, targetId, shooterId}
//	kill:      {type, targetId, shooterId}
//	shot:      {type, playerId, x, y}
//	bulletHit: {type, x, y}            — пуля ударилась о стену, границу или неуязвимого игрока
//	pickup:    {type, playerId, pickupType}
//	respawn:   {type, playerId, x, y}
//
// Теги нужны для разбора (клиенты на Go, тесты); omitempty на числах не
// годится — координата 0 и ID законны, поэтому форма задаётся типом.
type Event struct {
	Type       string  `json:"type"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	TargetID   uint32  `json:"targetId"`
	ShooterID  uint32  `json:"shooterId"`
	PlayerID   uint32  `json:"playerId"`
	PickupType string  `json:"pickupType"`
}

// MarshalJSON сериализует только поля, относящиеся к типу события.
func (e Event) MarshalJSON() ([]byte, error) {
	type xy struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	switch e.Type {
	case EventHit:
		return json.Marshal(struct {
			Type string `json:"type"`
			xy
			TargetID  uint32 `json:"targetId"`
			ShooterID uint32 `json:"shooterId"`
		}{e.Type, xy{e.X, e.Y}, e.TargetID, e.ShooterID})
	case EventKill:
		return json.Marshal(struct {
			Type      string `json:"type"`
			TargetID  uint32 `json:"targetId"`
			ShooterID uint32 `json:"shooterId"`
		}{e.Type, e.TargetID, e.ShooterID})
	case EventShot, EventRespawn:
		return json.Marshal(struct {
			Type     string `json:"type"`
			PlayerID uint32 `json:"playerId"`
			xy
		}{e.Type, e.PlayerID, xy{e.X, e.Y}})
	case EventBulletHit:
		return json.Marshal(struct {
			Type string `json:"type"`
			xy
		}{e.Type, xy{e.X, e.Y}})
	case EventPickup:
		return json.Marshal(struct {
			Type       string `json:"type"`
			PlayerID   uint32 `json:"playerId"`
			PickupType string `json:"pickupType"`
		}{e.Type, e.PlayerID, e.PickupType})
	}
	return nil, fmt.Errorf("net: unknown event type %q", e.Type)
}

// Фазы раунда (поле round.phase).
const (
	PhasePlay         = "play"
	PhaseIntermission = "intermission"
)

// Round — состояние раунда.
//
//	play:         {phase, timeLeft}
//	intermission: {phase, timeLeft, winnerId, scores}
//
// winnerId = 0 — победителя нет (ничья или ни одного фрага). Указатель нужен,
// чтобы в intermission поле присутствовало и с нулём, а в play отсутствовало.
type Round struct {
	Phase    string  `json:"phase"`
	TimeLeft float64 `json:"timeLeft"`
	WinnerID *uint32 `json:"winnerId,omitempty"`
	Scores   []Score `json:"scores,omitempty"`
}

// Score — итоговый счёт игрока в раунде.
type Score struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Snapshot — состояние мира, рассылаемое всем клиентам каждый тик.
// Bullets и Pickups всегда сериализуются массивами (не null);
// events без событий в тике опускается целиком.
type Snapshot struct {
	Type    string        `json:"type"`
	Tick    uint64        `json:"tick"`
	Players []PlayerState `json:"players"`
	Bullets []BulletState `json:"bullets"`
	Events  []Event       `json:"events,omitempty"`
	Pickups []Pickup      `json:"pickups"`
	Round   Round         `json:"round"`
}

// Типы пикапов (поле pickup.type).
const (
	PickupHealth = "health"
	PickupSpeed  = "speed"
	PickupAmmo   = "ammo"
)

// Pickup — пикап на карте.
type Pickup struct {
	ID   uint32  `json:"id"`
	Type string  `json:"type"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}
