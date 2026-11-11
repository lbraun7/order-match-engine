// Package observability exposes operational metrics without coupling the
// matching domain or order book to a monitoring vendor.
package observability

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/lbraun7/order-match-engine/internal/engine"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

const namespace = "order_match"

// Metrics owns the service collectors and provides the gRPC instrumentation
// interceptor. Register each Metrics value with exactly one registry.
type Metrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	engine   *engineCollector
}

// New creates metrics for request throughput/latency and aggregate engine
// state. Engine state is sampled when Prometheus scrapes the registry.
func New(router *engine.Router) *Metrics {
	return &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "grpc_requests_total",
			Help:      "Completed unary gRPC requests by method and status code.",
		}, []string{"method", "code"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "grpc_request_duration_seconds",
			Help:      "Unary gRPC request latency by method.",
			Buckets:   []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"method"}),
		engine: newEngineCollector(router),
	}
}

// Register adds all service collectors to the provided registry.
func (m *Metrics) Register(registerer prometheus.Registerer) error {
	for _, collector := range []prometheus.Collector{m.requests, m.latency, m.engine} {
		if err := registerer.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// UnaryServerInterceptor measures completed unary calls. Streaming duration is
// deliberately excluded because a market-data subscription can be long-lived.
func (m *Metrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		response, err := handler(ctx, req)
		m.requests.WithLabelValues(info.FullMethod, status.Code(err).String()).Inc()
		m.latency.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		return response, err
	}
}

type engineCollector struct {
	router         *engine.Router
	queueDepth     *prometheus.Desc
	queueCapacity  *prometheus.Desc
	bookLevels     *prometheus.Desc
	restingOrders  *prometheus.Desc
	sequence       *prometheus.Desc
	scrapeFailures *prometheus.Desc
	failures       atomic.Uint64
}

func newEngineCollector(router *engine.Router) *engineCollector {
	return &engineCollector{
		router:         router,
		queueDepth:     prometheus.NewDesc(namespace+"_engine_queue_depth", "Commands waiting in the single-writer queue.", []string{"symbol"}, nil),
		queueCapacity:  prometheus.NewDesc(namespace+"_engine_queue_capacity", "Configured command queue capacity.", []string{"symbol"}, nil),
		bookLevels:     prometheus.NewDesc(namespace+"_book_price_levels", "Visible price levels by side.", []string{"symbol", "side"}, nil),
		restingOrders:  prometheus.NewDesc(namespace+"_book_resting_orders", "Orders currently resting in the book.", []string{"symbol"}, nil),
		sequence:       prometheus.NewDesc(namespace+"_engine_sequence", "Latest engine sequence.", []string{"symbol"}, nil),
		scrapeFailures: prometheus.NewDesc(namespace+"_engine_stats_scrape_failures_total", "Engine samples that could not be collected.", nil, nil),
	}
}

func (c *engineCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queueDepth
	ch <- c.queueCapacity
	ch <- c.bookLevels
	ch <- c.restingOrders
	ch <- c.sequence
	ch <- c.scrapeFailures
}

func (c *engineCollector) Collect(ch chan<- prometheus.Metric) {
	for symbol, matchingEngine := range c.router.Engines() {
		label := string(symbol)
		queueDepth, queueCapacity := matchingEngine.QueueStats()
		ch <- prometheus.MustNewConstMetric(c.queueDepth, prometheus.GaugeValue, float64(queueDepth), label)
		ch <- prometheus.MustNewConstMetric(c.queueCapacity, prometheus.GaugeValue, float64(queueCapacity), label)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		stats, err := matchingEngine.Stats(ctx)
		cancel()
		if err != nil {
			c.failures.Add(1)
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.bookLevels, prometheus.GaugeValue, float64(stats.BidLevels), label, "buy")
		ch <- prometheus.MustNewConstMetric(c.bookLevels, prometheus.GaugeValue, float64(stats.AskLevels), label, "sell")
		ch <- prometheus.MustNewConstMetric(c.restingOrders, prometheus.GaugeValue, float64(stats.RestingOrders), label)
		ch <- prometheus.MustNewConstMetric(c.sequence, prometheus.GaugeValue, float64(stats.Sequence), label)
	}
	ch <- prometheus.MustNewConstMetric(c.scrapeFailures, prometheus.CounterValue, float64(c.failures.Load()))
}
