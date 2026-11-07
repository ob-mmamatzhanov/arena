package leaderboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTop = 10
	maxTop     = 100
	topTimeout = 2 * time.Second
)

// Handler отдаёт GET /leaderboard?n=10 → [{"name": …, "score": …}, …]
// (n по умолчанию 10, не больше 100). CORS разрешён тем же Origin, что и
// WebSocket: originPatterns — шаблоны хоста в формате nhooyr.io/websocket
// (например, "localhost:*"), плюс собственный хост сервера.
func Handler(st Store, originPatterns []string, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if o := r.Header.Get("Origin"); o != "" && originAllowed(o, r.Host, originPatterns) {
			w.Header().Set("Access-Control-Allow-Origin", o)
		}

		n := defaultTop
		if s := r.URL.Query().Get("n"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				http.Error(w, "n must be a positive integer", http.StatusBadRequest)
				return
			}
			n = min(v, maxTop)
		}

		ctx, cancel := context.WithTimeout(r.Context(), topTimeout)
		defer cancel()
		top, err := st.Top(ctx, n)
		if err != nil {
			log.Warn("leaderboard top", "err", err)
			http.Error(w, "leaderboard unavailable", http.StatusServiceUnavailable)
			return
		}
		if top == nil {
			top = []Entry{} // всегда массив, не null
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(top)
	})
}

// originAllowed повторяет проверку Origin в nhooyr.io/websocket: хост из
// Origin совпадает с хостом запроса или подходит под один из шаблонов
// (path.Match без учёта регистра).
func originAllowed(origin, host string, patterns []string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	h := strings.ToLower(u.Host)
	for _, p := range patterns {
		if ok, err := path.Match(strings.ToLower(p), h); err == nil && ok {
			return true
		}
	}
	return false
}
