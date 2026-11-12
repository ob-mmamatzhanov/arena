package leaderboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"arena/internal/cors"
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
		cors.Allow(w, r, originPatterns)

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
