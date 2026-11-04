package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	anet "arena/internal/net"
	"arena/internal/room"
)

func main() {
	addr := flag.String("addr", ":8080", "адрес HTTP-сервера")
	origins := flag.String("origins", "", "разрешённые Origin через запятую (например, localhost:5173)")
	static := flag.String("static", "", "каталог со статикой клиента, раздаётся на / (пусто — не раздавать)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lobby := room.New("lobby", log)
	roomCtx, stopRoom := context.WithCancel(context.Background())
	go lobby.Run(roomCtx)

	var originPatterns []string
	if *origins != "" {
		originPatterns = strings.Split(*origins, ",")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-lobby.Done():
			http.Error(w, "room stopped", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	})
	mux.Handle("GET /ws", room.Handler(lobby, anet.AcceptOptions{OriginPatterns: originPatterns}, log))
	if *static != "" {
		// "GET /" — наименее специфичный шаблон, /ws и /healthz имеют приоритет.
		mux.Handle("GET /", http.FileServer(http.Dir(*static)))
		log.Info("serving static", "dir", *static)
	}

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

	// Порядок: сначала комната закрывает WebSocket-соединения (они hijacked
	// и не отслеживаются http.Server), затем останавливаем HTTP-сервер.
	stopRoom()
	<-lobby.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	log.Info("bye")
}
