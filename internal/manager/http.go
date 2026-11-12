package manager

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"nhooyr.io/websocket"

	"arena/internal/cors"
	anet "arena/internal/net"
	"arena/internal/room"
)

// Close-коды WebSocket, которыми /ws отвечает до входа в комнату.
const (
	CloseBadRequest   websocket.StatusCode = 4000 // неизвестный mode
	CloseRoomNotFound websocket.StatusCode = 4004 // нет комнаты ?room=<id>
)

const (
	// CreateLimit комнат за CreateWindow с одного IP через POST /rooms.
	CreateLimit  = 5
	CreateWindow = time.Minute

	maxCreateBody = 1 << 10
)

// HTTPConfig — настройки HTTP-ручек менеджера.
type HTTPConfig struct {
	// OriginPatterns — разрешённые Origin (шаблоны хоста как у nhooyr.io/websocket)
	// и для WebSocket, и для CORS.
	OriginPatterns []string
	// ClientIPHeader — заголовок доверенного прокси с IP клиента (Fly-Client-IP)
	// для лимита POST /rooms; пусто — IP из RemoteAddr.
	ClientIPHeader string
	Log            *slog.Logger
}

// Register вешает на mux ручки менеджера:
//
//	GET  /rooms                → [{id, mode, phase, players, maxPlayers}] (только публичные)
//	POST /rooms {mode, maxPlayers, private} → 201 {id, code?}
//	GET  /rooms/by-code/{code} → {id} | 404
//	GET  /ws?room=<id> | ?mode=<mode> — WebSocket игрока
//	GET  /metrics              — метрики Prometheus
func (m *Manager) Register(mux *http.ServeMux, cfg HTTPConfig) {
	if cfg.Log == nil {
		cfg.Log = m.log
	}
	h := &handlers{m: m, cfg: cfg, limit: newLimiter(CreateLimit, CreateWindow, m.now)}
	mux.HandleFunc("GET /rooms", h.list)
	mux.HandleFunc("POST /rooms", h.create)
	mux.HandleFunc("OPTIONS /rooms", h.preflight)
	mux.HandleFunc("GET /rooms/by-code/{code}", h.byCode)
	mux.HandleFunc("GET /ws", h.ws)
	mux.Handle("GET /metrics", m.MetricsHandler())
}

type handlers struct {
	m     *Manager
	cfg   HTTPConfig
	limit *limiter
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	cors.Allow(w, r, h.cfg.OriginPatterns)
	writeJSON(w, http.StatusOK, h.m.List())
}

// preflight отвечает на CORS-preflight перед POST /rooms с JSON-телом.
func (h *handlers) preflight(w http.ResponseWriter, r *http.Request) {
	if cors.Allow(w, r, h.cfg.OriginPatterns) && r.Header.Get("Origin") != "" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	w.WriteHeader(http.StatusNoContent)
}

type createReq struct {
	Mode       string `json:"mode"`
	MaxPlayers int    `json:"maxPlayers"`
	Private    bool   `json:"private"`
}

type createResp struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	// Чужой Origin отклоняется до побочного эффекта: «простой» запрос
	// (text/plain) браузер отправит и без preflight.
	if !cors.Allow(w, r, h.cfg.OriginPatterns) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if ok, retry := h.limit.allow(clientIP(r, h.cfg.ClientIPHeader)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, "too many rooms created, try later", http.StatusTooManyRequests)
		return
	}

	var req createReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCreateBody)).Decode(&req); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	if !ValidMode(req.Mode) {
		http.Error(w, "mode must be arena or zombie", http.StatusBadRequest)
		return
	}
	if req.MaxPlayers < MinPlayers || req.MaxPlayers > room.MaxPlayers {
		http.Error(w, "maxPlayers must be 2-8", http.StatusBadRequest)
		return
	}

	rm, err := h.m.Create(Opts{Mode: req.Mode, MaxPlayers: req.MaxPlayers, Private: req.Private})
	switch {
	case errors.Is(err, ErrTooManyRooms), errors.Is(err, ErrClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		h.cfg.Log.Error("create room", "err", err)
		http.Error(w, "cannot create room", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, createResp{ID: rm.Name(), Code: h.m.Code(rm.Name())})
}

func (h *handlers) byCode(w http.ResponseWriter, r *http.Request) {
	cors.Allow(w, r, h.cfg.OriginPatterns)
	id, ok := h.m.ByCode(r.PathValue("code"))
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// ws подключает игрока: ?room=<id> — в конкретную комнату (нет её — close
// 4004), ?mode=<mode> — автозаполнение (FindOrCreate), без параметров —
// автозаполнение arena. Ошибки параметров сообщаются close-кодом после
// апгрейда: браузерный клиент не видит HTTP-статус неудачного апгрейда.
func (h *handlers) ws(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, mode := q.Get("room"), q.Get("mode")
	if mode == "" {
		mode = ModeArena
	}
	var target *room.Room
	if id != "" {
		target = h.m.Get(id)
	}

	conn, err := anet.Accept(w, r, anet.AcceptOptions{OriginPatterns: h.cfg.OriginPatterns})
	if err != nil {
		h.cfg.Log.Warn("ws accept", "err", err)
		return
	}
	switch {
	case id != "" && target == nil:
		conn.Close(CloseRoomNotFound, "room not found")
		return
	case id == "" && !ValidMode(mode):
		conn.Close(CloseBadRequest, "unknown mode")
		return
	}

	j, err := conn.ReadJoin(room.JoinTimeout())
	if err != nil {
		h.cfg.Log.Debug("no join", "err", err) // соединение уже закрыто
		return
	}

	// Место занимается после join: пока клиент молчит, оно не держится.
	if target != nil {
		if err := target.Reserve(); err != nil {
			code := room.CloseCode(err)
			if errors.Is(err, room.ErrClosed) { // комнату удалили, пока ждали join
				code = CloseRoomNotFound
			}
			conn.Close(code, err.Error())
			return
		}
	} else if target = h.m.FindOrCreate(mode); target == nil {
		conn.Close(websocket.StatusTryAgainLater, "no free rooms")
		return
	}
	room.Play(r.Context(), target, conn, j, h.cfg.Log)
}
