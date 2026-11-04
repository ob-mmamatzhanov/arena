// Package net описывает сетевой протокол и обёртку над WebSocket-соединением.
// Пакет не зависит от игровой логики: преобразование типов делает room.
package net

// Типы исходящих сообщений (поле "type").
const (
	TypeWelcome  = "welcome"
	TypeSnapshot = "snapshot"
)

// Input — сообщение клиент → сервер. Клиент шлёт его каждый свой кадр/тик
// (в том числе без изменений — это одновременно heartbeat).
type Input struct {
	Seq   uint32  `json:"seq"`
	MoveX float64 `json:"moveX"`
	MoveY float64 `json:"moveY"`
	Angle float64 `json:"angle"` // направление взгляда, радианы
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
	// Размеры мира — те же, что использует серверная симуляция.
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	// Параметры движения — нужны клиенту для client-side prediction,
	// чтобы его симуляция совпадала с серверной.
	PlayerSpeed  float64 `json:"speed"` // единиц в секунду
	PlayerRadius float64 `json:"radius"`
}

// PlayerState — состояние одного игрока в снапшоте.
type PlayerState struct {
	ID      uint32  `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Angle   float64 `json:"angle"`
	HP      int     `json:"hp"`
	Score   int     `json:"score"`
	LastSeq uint32  `json:"lastSeq"`
}

// Snapshot — состояние мира, рассылаемое всем клиентам каждый тик.
type Snapshot struct {
	Type    string        `json:"type"`
	Tick    uint64        `json:"tick"`
	Players []PlayerState `json:"players"`
}
