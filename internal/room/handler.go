package room

import (
	"errors"
	"log/slog"
	"net/http"

	"nhooyr.io/websocket"

	anet "arena/internal/net"
)

// Handler возвращает HTTP-обработчик, апгрейдящий запрос до WebSocket
// и подключающий игрока к комнате rm.
func Handler(rm *Room, opts anet.AcceptOptions, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := anet.Accept(w, r, opts)
		if err != nil {
			log.Warn("ws accept", "err", err)
			return
		}

		id, err := rm.Join(r.Context(), conn)
		if err != nil {
			code := websocket.StatusTryAgainLater
			if errors.Is(err, ErrClosed) {
				code = websocket.StatusGoingAway
			}
			conn.Close(code, err.Error())
			return
		}

		go func() { _ = conn.WritePump() }()

		err = conn.ReadPump(func(in anet.Input) {
			rm.PushInput(anet.PlayerInput{PlayerID: id, Input: in})
		})
		rm.Leave(id)
		conn.Close(websocket.StatusNormalClosure, "")
		if s := websocket.CloseStatus(err); s != websocket.StatusNormalClosure && s != websocket.StatusGoingAway {
			log.Debug("read pump ended", "room", rm.Name(), "player", id, "err", err)
		}
	})
}
