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
	Fire  bool    `json:"fire"`  // огонь зажат
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
	// Obstacles статичны, поэтому отдаются один раз здесь и не дублируются
	// в снапшотах. Всегда массив (не null).
	Obstacles []Obstacle `json:"obstacles"`
}

// Obstacle — препятствие карты: левый верхний угол (x, y), ширина w, высота h.
type Obstacle struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// PlayerState — состояние одного игрока в снапшоте.
type PlayerState struct {
	ID      uint32  `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Angle   float64 `json:"angle"`
	Heat    float64 `json:"heat"` // перегрев [0, 1]: разброс ±heat×0.25 рад, для прицела
	HP      int     `json:"hp"`
	Score   int     `json:"score"`
	LastSeq uint32  `json:"lastSeq"`
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

// Hit — попадание, случившееся в тике этого снапшота.
type Hit struct {
	Target  uint32  `json:"target"`
	Shooter uint32  `json:"shooter"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Killed  bool    `json:"killed"`
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
	Score int    `json:"score"`
}

// Snapshot — состояние мира, рассылаемое всем клиентам каждый тик.
// Bullets и Hits всегда сериализуются массивами (не null).
type Snapshot struct {
	Type    string        `json:"type"`
	Tick    uint64        `json:"tick"`
	Players []PlayerState `json:"players"`
	Bullets []BulletState `json:"bullets"`
	Hits    []Hit         `json:"hits"`
	Round   Round         `json:"round"`
}
