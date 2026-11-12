package manager

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics — метрики Prometheus менеджера в собственном реестре (не в
// глобальном: в тестах менеджеров много, и повторная регистрация паниковала бы).
//
//	arena_rooms_active{mode}      — живые комнаты по режимам (gauge)
//	arena_players_online          — подключённые игроки во всех комнатах (gauge)
//	arena_tick_duration_seconds{room} — длительность тика (histogram)
//
// Gauge-метрики не хранятся, а считаются из реестра при каждом сборе, поэтому
// не могут разойтись с ним. Серия гистограммы удаляется вместе с комнатой.
type metrics struct {
	reg     *prometheus.Registry
	tick    *prometheus.HistogramVec
	rooms   *prometheus.Desc
	players *prometheus.Desc
	m       *Manager
}

func newMetrics(m *Manager) *metrics {
	mt := &metrics{
		reg: prometheus.NewRegistry(),
		tick: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "arena_tick_duration_seconds",
			Help: "Длительность тика комнаты.",
			// 50 мкс … ~100 мс: бюджет тика при 20 Гц — 50 мс.
			Buckets: prometheus.ExponentialBuckets(50e-6, 2, 12),
		}, []string{"room"}),
		rooms: prometheus.NewDesc("arena_rooms_active",
			"Живые комнаты по режимам.", []string{"mode"}, nil),
		players: prometheus.NewDesc("arena_players_online",
			"Подключённые игроки во всех комнатах.", nil, nil),
		m: m,
	}
	mt.reg.MustRegister(mt.tick, mt,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return mt
}

// Describe и Collect реализуют prometheus.Collector для gauge-метрик реестра.
func (mt *metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- mt.rooms
	ch <- mt.players
}

func (mt *metrics) Collect(ch chan<- prometheus.Metric) {
	byMode := make(map[string]int, len(Modes))
	players := 0
	mt.m.mu.RLock()
	for _, e := range mt.m.rooms {
		byMode[e.room.Mode()]++
		players += e.room.Players()
	}
	mt.m.mu.RUnlock()
	for _, mode := range Modes { // все режимы, включая нулевые
		ch <- prometheus.MustNewConstMetric(mt.rooms, prometheus.GaugeValue, float64(byMode[mode]), mode)
	}
	ch <- prometheus.MustNewConstMetric(mt.players, prometheus.GaugeValue, float64(players))
}

func (mt *metrics) tickObserver(room string) prometheus.Observer {
	return mt.tick.WithLabelValues(room)
}

func (mt *metrics) forget(room string) {
	mt.tick.DeleteLabelValues(room)
}

// MetricsHandler отдаёт метрики в текстовом формате Prometheus.
func (m *Manager) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(m.metrics.reg, promhttp.HandlerOpts{})
}
