package room

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	anet "arena/internal/net"
)

// joinTimeout — сколько ждать сообщения join после апгрейда. Переменная, а не
// константа, — чтобы тесты могли его укоротить.
var joinTimeout = 5 * time.Second

// JoinTimeout — сколько обработчики ждут первое сообщение join.
func JoinTimeout() time.Duration { return joinTimeout }

// Handler возвращает HTTP-обработчик, апгрейдящий запрос до WebSocket
// и подключающий игрока к единственной комнате rm. Первым сообщением клиент
// обязан прислать join (см. anet.Join), иначе соединение закрывается.
// Несколько комнат обслуживает manager, он использует Play напрямую.
func Handler(rm *Room, opts anet.AcceptOptions, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := anet.Accept(w, r, opts)
		if err != nil {
			log.Warn("ws accept", "err", err)
			return
		}

		j, err := conn.ReadJoin(joinTimeout)
		if err != nil {
			log.Debug("no join", "room", rm.Name(), "err", err) // соединение уже закрыто
			return
		}

		if err := rm.Reserve(); err != nil {
			conn.Close(CloseCode(err), err.Error())
			return
		}
		Play(r.Context(), rm, conn, j, log)
	})
}

// CloseCode — close-код WebSocket для ошибки входа в комнату: ErrClosed —
// GoingAway, остальное (ErrFull и пр.) — TryAgainLater.
func CloseCode(err error) websocket.StatusCode {
	if errors.Is(err, ErrClosed) {
		return websocket.StatusGoingAway
	}
	return websocket.StatusTryAgainLater
}

// Play подключает принятое соединение с уже прочитанным join к комнате rm,
// в которой для него занято место (Reserve), и обслуживает его до
// отключения. Блокирует; место освобождается в любом случае.
func Play(ctx context.Context, rm *Room, conn *anet.Conn, j anet.Join, log *slog.Logger) {
	id, err := rm.JoinReserved(ctx, conn, j)
	if err != nil {
		conn.Close(CloseCode(err), err.Error())
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
}
