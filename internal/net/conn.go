package net

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sendBuffer    = 32               // ~1.6 с снапшотов при 20 Hz
	readLimit     = 1 << 10          // входящие сообщения маленькие
	readTimeout   = 10 * time.Second // клиент обязан слать Input регулярно
	writeTimeout  = 5 * time.Second
	closeDeadline = 5 * time.Second
)

// ErrClosed возвращается при работе с закрытым соединением.
var ErrClosed = errors.New("net: connection closed")

// Conn — WebSocket-соединение с буферизованной очередью отправки.
// Писать в сокет может только WritePump; остальные кладут данные через TrySend.
type Conn struct {
	ws     *websocket.Conn
	send   chan []byte
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// AcceptOptions — настройки апгрейда HTTP → WebSocket.
type AcceptOptions struct {
	// OriginPatterns — разрешённые Origin помимо собственного хоста (см. github.com/coder/websocket).
	OriginPatterns []string
}

// Accept апгрейдит HTTP-запрос до WebSocket.
func Accept(w http.ResponseWriter, r *http.Request, opts AcceptOptions) (*Conn, error) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: opts.OriginPatterns,
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(readLimit)

	// Контекст соединения не наследуется от r.Context(): временем жизни
	// управляет сам Conn (Close) и комната.
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{
		ws:     ws,
		send:   make(chan []byte, sendBuffer),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Done закрывается, когда соединение закрыто.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// TrySend кладёт сообщение в очередь без блокировки.
// false — очередь переполнена (медленный клиент) или соединение закрыто.
// Срез msg не должен изменяться после вызова: он может разделяться между клиентами.
func (c *Conn) TrySend(msg []byte) bool {
	if c.ctx.Err() != nil {
		return false
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

// SendJSON сериализует v и вызывает TrySend.
func (c *Conn) SendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if !c.TrySend(b) {
		return ErrClosed
	}
	return nil
}

// Close закрывает соединение. Не блокирует (handshake закрытия идёт в фоне),
// поэтому безопасен для вызова из горутины комнаты. Идемпотентен.
func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		c.cancel()
		go func() {
			done := make(chan struct{})
			go func() {
				_ = c.ws.Close(code, reason)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(closeDeadline):
				_ = c.ws.CloseNow()
			}
		}()
	})
}

// ErrNotJoin — первое сообщение клиента не join.
var ErrNotJoin = errors.New("net: first message is not join")

// ErrJoinTimeout — клиент не прислал join вовремя.
var ErrJoinTimeout = errors.New("net: join timeout")

// ReadJoin ждёт первое сообщение клиента (Join) не дольше timeout. При
// ошибке соединение закрывается: по таймауту и при сообщении другого типа —
// с кодом PolicyViolation, при нераспознанном JSON — UnsupportedData.
// Вызывается до ReadPump в той же горутине.
//
// Таймаут — таймером через Close, а не контекстом Read: по истечении
// контекста coder/websocket рвёт соединение без close-кода (см. ReadPump).
func (c *Conn) ReadJoin(timeout time.Duration) (Join, error) {
	timer := time.AfterFunc(timeout, func() {
		c.Close(websocket.StatusPolicyViolation, "join timeout")
	})
	typ, data, err := c.ws.Read(context.Background())
	if !timer.Stop() {
		return Join{}, ErrJoinTimeout
	}
	if err != nil {
		c.Close(websocket.StatusPolicyViolation, "join failed")
		return Join{}, err
	}
	var j Join
	if typ != websocket.MessageText {
		c.Close(websocket.StatusUnsupportedData, "bad join")
		return Join{}, ErrNotJoin
	}
	if err := json.Unmarshal(data, &j); err != nil {
		c.Close(websocket.StatusUnsupportedData, "bad join")
		return Join{}, err
	}
	if j.Type != TypeJoin {
		c.Close(websocket.StatusPolicyViolation, "join expected")
		return Join{}, ErrNotJoin
	}
	return j, nil
}

// ReadPump читает Input-сообщения и передаёт их в onInput до ошибки или закрытия.
// Блокирует; вызывается в горутине HTTP-обработчика.
//
// Таймауты чтения и записи намеренно не наследуются от c.ctx: при отмене
// контекста coder/websocket сам шлёт close-фрейм (PolicyViolation) или рвёт TCP, что
// перебило бы код из Close. Прерывание операций обеспечивает ws.Close.
func (c *Conn) ReadPump(onInput func(Input)) error {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		typ, data, err := c.ws.Read(ctx)
		cancel()
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var in Input
		if err := json.Unmarshal(data, &in); err != nil {
			c.Close(websocket.StatusUnsupportedData, "bad input")
			return err
		}
		onInput(in)
	}
}

// WritePump отправляет сообщения из очереди в сокет до закрытия соединения.
// Блокирует; запускается в отдельной горутине.
func (c *Conn) WritePump() error {
	for {
		select {
		case <-c.ctx.Done():
			return ErrClosed
		case msg := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				c.Close(websocket.StatusInternalError, "write failed")
				return err
			}
		}
	}
}
