// Package manager — реестр игровых комнат над пакетом room: создание,
// поиск комнаты для автозаполнения, приватные коды, удаление пустых комнат,
// HTTP-ручки /rooms, /ws, /metrics.
//
// Реестр (map id → комната) защищён одним sync.RWMutex — это единственный
// мьютекс менеджера. Внутрь комнат менеджер не лезет: каждая комната — своя
// горутина, а наружу она публикует только атомарные счётчики (Players, Seats,
// Phase), которые менеджер читает без блокировок комнаты.
package manager

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"arena/internal/game"
	"arena/internal/leaderboard"
	"arena/internal/room"
)

// Режимы комнат.
const (
	ModeArena  = "arena"
	ModeZombie = "zombie"
)

// Modes — все допустимые режимы в порядке вывода метрик.
var Modes = []string{ModeArena, ModeZombie}

const (
	// MaxRooms — сколько комнат может жить одновременно.
	MaxRooms = 50
	// MinPlayers — нижняя граница вместимости при создании комнаты.
	MinPlayers = 2
	// EmptyTTL — сколько комната может простоять без игроков до удаления.
	EmptyTTL = 30 * time.Second
	// ReapInterval — период проверки пустых комнат.
	ReapInterval = 5 * time.Second

	idLen   = 8
	codeLen = 4
	// codeAlphabet — заглавные буквы без неоднозначных O и I (цифр в коде нет вовсе).
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	// genAttempts — сколько раз пытаться получить свободный id/код.
	genAttempts = 100
)

var (
	ErrTooManyRooms  = errors.New("manager: too many rooms")
	ErrBadMode       = errors.New("manager: unknown mode")
	ErrBadMaxPlayers = errors.New("manager: maxPlayers must be 2-8")
	ErrClosed        = errors.New("manager: shut down")
	errNoFreeKey     = errors.New("manager: no free id or code")
)

// ValidMode сообщает, известен ли режим.
func ValidMode(mode string) bool { return slices.Contains(Modes, mode) }

// gameConfig — конфиг мира для режима. Отдельного zombie-режима в симуляции
// пока нет: такая комната запускает обычную арену и отличается только меткой.
func gameConfig(mode string) game.Config {
	switch mode {
	case ModeZombie:
		return game.DefaultConfig() // TODO: правила zombie-режима
	default:
		return game.DefaultConfig()
	}
}

// Opts — параметры новой комнаты.
type Opts struct {
	Mode       string // ModeArena или ModeZombie
	MaxPlayers int    // 2…8; 0 — room.MaxPlayers
	Private    bool   // не видна в List и автозаполнении, получает код
}

// RoomInfo — публичное описание комнаты для GET /rooms.
type RoomInfo struct {
	ID         string `json:"id"`
	Mode       string `json:"mode"`
	Phase      string `json:"phase"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"maxPlayers"`
}

// Config — зависимости менеджера.
type Config struct {
	Store leaderboard.Store // nil — фраги не записываются
	Log   *slog.Logger
	Now   func() time.Time // часы для удаления пустых комнат; nil — time.Now
}

type entry struct {
	room    *room.Room
	cancel  context.CancelFunc
	private bool
	code    string
	// emptySince — с какого момента комната замечена пустой; нулевое — в ней
	// кто-то есть (игрок или резерв места).
	emptySince time.Time
}

// Manager — реестр комнат. Методы безопасны для вызова из любых горутин.
type Manager struct {
	log     *slog.Logger
	store   leaderboard.Store
	now     func() time.Time
	metrics *metrics

	// Генераторы id и кода; подменяются в тестах.
	newID   func() string
	newCode func() string

	mu     sync.RWMutex
	rooms  map[string]*entry
	order  []string          // id в порядке создания: «первая подходящая» детерминирована
	codes  map[string]string // код приватной комнаты → id
	closed bool

	stopOnce sync.Once
	stop     chan struct{} // закрывается в Shutdown
}

// New создаёт менеджер и запускает фоновое удаление пустых комнат.
func New(cfg Config) *Manager {
	m := newManager(cfg)
	go m.reapLoop()
	return m
}

func newManager(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := &Manager{
		log:     cfg.Log,
		store:   cfg.Store,
		now:     cfg.Now,
		newID:   randomID,
		newCode: randomCode,
		rooms:   make(map[string]*entry),
		codes:   make(map[string]string),
		stop:    make(chan struct{}),
	}
	m.metrics = newMetrics(m)
	return m
}

// Done закрывается, когда менеджер остановлен (Shutdown).
func (m *Manager) Done() <-chan struct{} { return m.stop }

// Create создаёт и запускает комнату. Ошибки: ErrBadMode, ErrBadMaxPlayers,
// ErrTooManyRooms, ErrClosed.
func (m *Manager) Create(o Opts) (*room.Room, error) {
	if !ValidMode(o.Mode) {
		return nil, ErrBadMode
	}
	if o.MaxPlayers == 0 {
		o.MaxPlayers = room.MaxPlayers
	}
	if o.MaxPlayers < MinPlayers || o.MaxPlayers > room.MaxPlayers {
		return nil, ErrBadMaxPlayers
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.createLocked(o)
	if err != nil {
		return nil, err
	}
	return e.room, nil
}

// createLocked создаёт комнату; вызывается под m.mu на запись с проверенными opts.
func (m *Manager) createLocked(o Opts) (*entry, error) {
	if m.closed {
		return nil, ErrClosed
	}
	if len(m.rooms) >= MaxRooms {
		return nil, ErrTooManyRooms
	}
	id, err := unique(m.newID, func(s string) bool { _, ok := m.rooms[s]; return ok })
	if err != nil {
		return nil, err
	}
	var code string
	if o.Private {
		if code, err = unique(m.newCode, func(s string) bool { _, ok := m.codes[s]; return ok }); err != nil {
			return nil, err
		}
	}

	obs := m.metrics.tickObserver(id)
	rm := room.New(id, m.store, m.log, room.WithMode(o.Mode), room.WithMaxPlayers(o.MaxPlayers),
		room.WithGameConfig(gameConfig(o.Mode)),
		room.WithTickObserver(func(d time.Duration) { obs.Observe(d.Seconds()) }))
	ctx, cancel := context.WithCancel(context.Background())
	go rm.Run(ctx)

	e := &entry{room: rm, cancel: cancel, private: o.Private, code: code, emptySince: m.now()}
	m.rooms[id] = e
	m.order = append(m.order, id)
	if code != "" {
		m.codes[code] = id
	}
	m.log.Info("room created", "room", id, "mode", o.Mode, "maxPlayers", o.MaxPlayers,
		"private", o.Private, "rooms", len(m.rooms))
	return e, nil
}

// unique вызывает gen, пока taken не вернёт false.
func unique(gen func() string, taken func(string) bool) (string, error) {
	for range genAttempts {
		if s := gen(); !taken(s) {
			return s, nil
		}
	}
	return "", errNoFreeKey
}

// Get возвращает комнату по id; nil — нет такой.
func (m *Manager) Get(id string) *room.Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.rooms[id]; ok {
		return e.room
	}
	return nil
}

// Code возвращает код приватной комнаты ("" у публичной или несуществующей).
func (m *Manager) Code(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.rooms[id]; ok {
		return e.code
	}
	return ""
}

// ByCode возвращает id приватной комнаты по коду (без учёта регистра).
func (m *Manager) ByCode(code string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.codes[strings.ToUpper(code)]
	return id, ok
}

// List возвращает публичные комнаты в порядке создания.
func (m *Manager) List() []RoomInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RoomInfo, 0, len(m.order))
	for _, id := range m.order {
		e := m.rooms[id]
		if e.private {
			continue
		}
		out = append(out, info(e.room))
	}
	return out
}

func info(rm *room.Room) RoomInfo {
	return RoomInfo{
		ID:         rm.Name(),
		Mode:       rm.Mode(),
		Phase:      string(rm.Phase()),
		Players:    rm.Players(),
		MaxPlayers: rm.MaxPlayers(),
	}
}

// FindOrCreate возвращает первую (по времени создания) публичную комнату
// режима mode в фазе play или intermission со свободным местом, иначе
// создаёт новую. Место в возвращённой комнате уже занято (room.Reserve):
// вызывающий обязан либо войти через JoinReserved, либо вернуть его через
// Unreserve. Так параллельные вызовы не переполняют комнату и не плодят
// лишних. nil — неизвестный режим, лимит комнат или менеджер остановлен.
func (m *Manager) FindOrCreate(mode string) *room.Room {
	if !ValidMode(mode) {
		return nil
	}
	// Запись, а не чтение: поиск и создание должны быть одной операцией,
	// иначе два вызова одновременно создадут по комнате.
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		e := m.rooms[id]
		if e.private || e.room.Mode() != mode || !joinablePhase(e.room.Phase()) || stopped(e.room) {
			continue
		}
		if e.room.Reserve() == nil {
			return e.room
		}
	}
	e, err := m.createLocked(Opts{Mode: mode, MaxPlayers: room.MaxPlayers})
	if err != nil {
		m.log.Warn("autofill: cannot create room", "mode", mode, "err", err)
		return nil
	}
	if err := e.room.Reserve(); err != nil { // новая комната пуста и не списана
		m.log.Error("autofill: reserve in new room", "room", e.room.Name(), "err", err)
		return nil
	}
	return e.room
}

func joinablePhase(p game.Phase) bool {
	return p == game.PhasePlay || p == game.PhaseIntermission
}

func stopped(rm *room.Room) bool {
	select {
	case <-rm.Done():
		return true
	default:
		return false
	}
}

func (m *Manager) reapLoop() {
	t := time.NewTicker(ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reap()
		}
	}
}

// reap удаляет комнаты, простоявшие без игроков и резервов EmptyTTL, а
// также остановившиеся сами. Пустота замечается при проверке, поэтому
// фактически комната живёт EmptyTTL…EmptyTTL+ReapInterval после ухода
// последнего игрока; новая комната считается пустой с момента создания.
func (m *Manager) reap() {
	now := m.now()
	var stop []context.CancelFunc
	m.mu.Lock()
	for _, id := range slices.Clone(m.order) {
		e := m.rooms[id]
		switch {
		case stopped(e.room):
		case e.room.Seats() > 0:
			e.emptySince = time.Time{}
			continue
		case e.emptySince.IsZero():
			e.emptySince = now
			continue
		case now.Sub(e.emptySince) < EmptyTTL:
			continue
		case !e.room.Retire(): // кто-то занял место между Seats и Retire
			continue
		}
		m.removeLocked(id)
		stop = append(stop, e.cancel)
		m.log.Info("empty room removed", "room", id, "rooms", len(m.rooms))
	}
	m.mu.Unlock()
	for _, cancel := range stop {
		cancel() // комната закроется сама; ждать её здесь незачем
	}
}

func (m *Manager) removeLocked(id string) {
	e := m.rooms[id]
	delete(m.rooms, id)
	if e.code != "" {
		delete(m.codes, e.code)
	}
	m.order = slices.DeleteFunc(m.order, func(s string) bool { return s == id })
	m.metrics.forget(id)
}

// Shutdown останавливает все комнаты (они закрывают соединения игроков) и
// ждёт их завершения, но не дольше ctx. После него Create и FindOrCreate
// не создают комнат. Повторный вызов только ждёт уже остановленные.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	entries := make([]*entry, 0, len(m.rooms))
	for _, id := range slices.Clone(m.order) { // removeLocked правит m.order
		entries = append(entries, m.rooms[id])
		m.removeLocked(id)
	}
	m.mu.Unlock()
	m.stopOnce.Do(func() { close(m.stop) })

	for _, e := range entries {
		e.cancel()
	}
	for _, e := range entries {
		select {
		case <-e.room.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// randomID — 8 символов base32 (40 бит) из crypto/rand.
func randomID() string { return strings.ToLower(rand.Text()[:idLen]) }

// randomCode — 4 заглавные буквы без O и I.
func randomCode() string {
	n := big.NewInt(int64(len(codeAlphabet)))
	b := make([]byte, codeLen)
	for i := range b {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			panic(err) // crypto/rand не возвращает ошибок (Go ≥ 1.24)
		}
		b[i] = codeAlphabet[k.Int64()]
	}
	return string(b)
}
