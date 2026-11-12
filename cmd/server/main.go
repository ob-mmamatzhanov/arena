package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"arena/internal/leaderboard"
	"arena/internal/manager"
)

func main() {
	addr := flag.String("addr", ":8080", "адрес HTTP-сервера")
	origins := flag.String("origins", "", "разрешённые Origin через запятую (например, localhost:5173)")
	redisURL := flag.String("redis", os.Getenv("REDIS_URL"),
		"URL Redis для таблицы лидеров (redis://[user:pass@]host:port/db); пусто — в памяти. По умолчанию $REDIS_URL")
	ipHeader := flag.String("ip-header", "",
		"заголовок доверенного прокси с IP клиента для лимита POST /rooms (например, Fly-Client-IP); пусто — RemoteAddr")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Недоступный Redis не мешает старту: Open откатывается на память.
	lb := leaderboard.Open(ctx, *redisURL, log)
	if c, ok := lb.(io.Closer); ok {
		defer c.Close()
	}

	rooms := manager.New(manager.Config{Store: lb, Log: log})

	var originPatterns []string
	if *origins != "" {
		originPatterns = strings.Split(*origins, ",")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-rooms.Done():
			http.Error(w, "manager stopped", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	})
	rooms.Register(mux, manager.HTTPConfig{OriginPatterns: originPatterns, ClientIPHeader: *ipHeader, Log: log})
	mux.Handle("GET /leaderboard", leaderboard.Handler(lb, originPatterns, log))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
		}
	}

	// Порядок: сначала комнаты закрывают WebSocket-соединения (они hijacked
	// и не отслеживаются http.Server), затем останавливаем HTTP-сервер.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rooms.Shutdown(shutdownCtx); err != nil {
		log.Error("rooms shutdown", "err", err)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	log.Info("bye")
}
