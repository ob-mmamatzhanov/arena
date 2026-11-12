package net

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Регрессия: сервер закрывает соединение кодом (как ?room=<нет> → 4004 или
// полная комната → 1013), клиент — как браузер — сразу шлёт join, а на close
// отвечает своим close. Сервер обязан прислать ровно один close-фрейм: второй
// (эхо ответа клиента) Chrome считает нарушением протокола и вместо кода
// отдаёт странице 1006 — так вела себя nhooyr.io/websocket v1.8.17.
func TestCloseSendsSingleCloseFrame(t *testing.T) {
	const code, reason = websocket.StatusCode(4004), "room not found"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r, AcceptOptions{})
		if err != nil {
			return
		}
		conn.Close(code, reason) // как в manager: Close и сразу возврат из обработчика
	}))
	defer srv.Close()

	c, err := stdnet.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	key := make([]byte, 16)
	_, _ = rand.Read(key)
	fmt.Fprintf(c, "GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n",
		strings.TrimPrefix(srv.URL, "http://"), base64.StdEncoding.EncodeToString(key))

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("апгрейд: статус %d", resp.StatusCode)
	}
	// Браузер шлёт join сразу после open, не дожидаясь сообщений сервера.
	if _, err := c.Write(clientFrame(0x1, []byte(`{"type":"join","name":"x","playerKey":""}`))); err != nil {
		t.Fatal(err)
	}

	var closes []string
	for {
		op, payload, err := readFrame(br)
		if err != nil {
			break // EOF или RST после закрытия — нормально, важно только число close-фреймов
		}
		if op != 0x8 {
			continue
		}
		got := fmt.Sprintf("%d %q", binary.BigEndian.Uint16(payload), payload[2:])
		closes = append(closes, got)
		if len(closes) == 1 {
			// Ответ клиента — как у браузера: тот же код без причины.
			_, _ = c.Write(clientFrame(0x8, payload[:2]))
		}
	}

	want := fmt.Sprintf("%d %q", code, reason)
	if len(closes) != 1 || closes[0] != want {
		t.Fatalf("close-фреймы от сервера = %v, нужен ровно один: %s", closes, want)
	}
}

// clientFrame — замаскированный кадр клиента (FIN, payload < 126 байт).
func clientFrame(op byte, payload []byte) []byte {
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	b := []byte{0x80 | op, 0x80 | byte(len(payload))}
	b = append(b, mask...)
	for i, p := range payload {
		b = append(b, p^mask[i%4])
	}
	return b
}

// readFrame читает незамаскированный кадр сервера (payload < 126 байт — для теста хватает).
func readFrame(br *bufio.Reader) (op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(br, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(h[1] & 0x7f)
	if n >= 126 {
		return 0, nil, fmt.Errorf("длинный кадр %d не ожидается", n)
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	return h[0] & 0x0f, payload, nil
}
