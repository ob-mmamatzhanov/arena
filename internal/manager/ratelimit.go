package manager

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter — скользящее окно: не больше limit событий за window на ключ (IP).
// Собственный мьютекс — это HTTP-слой, к реестру комнат он не относится.
type limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	hits      map[string][]time.Time // моменты событий в окне, по возрастанию
	lastPrune time.Time
}

func newLimiter(limit int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{limit: limit, window: window, now: now, hits: make(map[string][]time.Time)}
}

// allow учитывает событие для key. false — лимит исчерпан; тогда retry —
// через сколько освободится место.
func (l *limiter) allow(key string) (ok bool, retry time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastPrune) >= l.window { // не чаще раза в окно: O(ключей)
		l.lastPrune = now
		for k, ts := range l.hits {
			if now.Sub(ts[len(ts)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	ts := l.hits[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= l.window {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.limit {
		l.hits[key] = ts
		return false, l.window - now.Sub(ts[0])
	}
	l.hits[key] = append(ts, now)
	return true, 0
}

// clientIP — адрес клиента: из заголовка header (его выставляет доверенный
// прокси, например Fly-Client-IP), если он задан и есть в запросе, иначе
// из RemoteAddr. Заголовку без прокси доверять нельзя — клиент подделает его.
func clientIP(r *http.Request, header string) string {
	if header != "" {
		if v := r.Header.Get(header); v != "" {
			first, _, _ := strings.Cut(v, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
