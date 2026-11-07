// Package leaderboard хранит накопленный за всё время счёт игроков.
//
// Игрок идентифицируется постоянным ключом (playerKey из сообщения join),
// а имя хранится отдельно и перезаписывается последним присланным. Пакет не
// зависит от игровой логики: счёт в него пишет room, читает HTTP-обработчик.
package leaderboard

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry — строка таблицы лидеров.
type Entry struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Store — хранилище таблицы лидеров. Реализации потокобезопасны.
type Store interface {
	// AddScore прибавляет score к счёту игрока key и запоминает его имя.
	AddScore(ctx context.Context, key, name string, score int) error
	// Top возвращает до n лучших игроков по убыванию счёта.
	Top(ctx context.Context, n int) ([]Entry, error)
}

// Memory — Store в памяти процесса: теряется при перезапуске.
type Memory struct {
	mu      sync.Mutex
	players map[string]Entry // key → имя и счёт
}

// NewMemory создаёт пустое in-memory хранилище.
func NewMemory() *Memory {
	return &Memory{players: make(map[string]Entry)}
}

func (m *Memory) AddScore(_ context.Context, key, name string, score int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.players[key]
	m.players[key] = Entry{Name: name, Score: e.Score + score}
	return nil
}

func (m *Memory) Top(_ context.Context, n int) ([]Entry, error) {
	if n <= 0 {
		return []Entry{}, nil
	}
	m.mu.Lock()
	type row struct {
		key string
		Entry
	}
	rows := make([]row, 0, len(m.players))
	for k, e := range m.players {
		rows = append(rows, row{k, e})
	}
	m.mu.Unlock()

	// При равном счёте — по ключу, как ZREVRANGE в Redis (там — по убыванию).
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].key > rows[j].key
	})
	out := make([]Entry, 0, min(n, len(rows)))
	for _, r := range rows[:min(n, len(rows))] {
		out = append(out, r.Entry)
	}
	return out, nil
}

// Ключи Redis: счёт — sorted set (member = playerKey), имена — hash.
const (
	redisScoresKey = "lb:scores"
	redisNamesKey  = "lb:names"
)

// Redis — Store поверх Redis: ZINCRBY lb:scores и HSET lb:names.
type Redis struct {
	rdb *redis.Client
}

// NewRedis оборачивает готовый клиент. Подключение не проверяется — см. Open.
func NewRedis(rdb *redis.Client) *Redis { return &Redis{rdb: rdb} }

func (r *Redis) AddScore(ctx context.Context, key, name string, score int) error {
	// MULTI/EXEC: счёт и имя меняются вместе.
	_, err := r.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZIncrBy(ctx, redisScoresKey, float64(score), key)
		p.HSet(ctx, redisNamesKey, key, name)
		return nil
	})
	return err
}

func (r *Redis) Top(ctx context.Context, n int) ([]Entry, error) {
	if n <= 0 {
		return []Entry{}, nil
	}
	zs, err := r.rdb.ZRevRangeWithScores(ctx, redisScoresKey, 0, int64(n-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(zs))
	if len(zs) == 0 {
		return out, nil
	}
	keys := make([]string, len(zs))
	for i, z := range zs {
		keys[i], _ = z.Member.(string)
		out[i].Score = int(z.Score)
	}
	names, err := r.rdb.HMGet(ctx, redisNamesKey, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, v := range names {
		out[i].Name, _ = v.(string) // nil — имени нет; ключ не раскрываем
	}
	return out, nil
}

// Close закрывает соединение с Redis.
func (r *Redis) Close() error { return r.rdb.Close() }

// openTimeout — сколько ждать ответа Redis при старте.
const openTimeout = 3 * time.Second

// Open выбирает хранилище по URL Redis. Пустой URL — Memory. Если URL
// некорректен или Redis не отвечает, ошибка логируется и возвращается Memory:
// игра работает и без постоянной таблицы лидеров. В лог попадает только
// хост Redis, без логина и пароля.
func Open(ctx context.Context, rawURL string, log *slog.Logger) Store {
	if rawURL == "" {
		log.Info("leaderboard store", "store", "memory")
		return NewMemory()
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		// Текст ошибки разбора может содержать URL целиком, вместе с паролем.
		log.Error("leaderboard store: invalid redis URL, falling back to memory", "store", "memory")
		return NewMemory()
	}
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		log.Error("leaderboard store: redis unavailable, falling back to memory",
			"host", opts.Addr, "err", err, "store", "memory")
		return NewMemory()
	}
	log.Info("leaderboard store", "store", "redis", "host", opts.Addr)
	return NewRedis(rdb)
}
