package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"github.com/lucasbraun96/order-match-engine/internal/grpcserver"
	"github.com/lucasbraun96/order-match-engine/internal/observability"
	"github.com/lucasbraun96/order-match-engine/internal/projection"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

func main() {
	address := flag.String("address", ":50051", "gRPC listen address")
	observabilityAddress := flag.String("observability-address", ":9090", "HTTP address for /metrics and /debug/pprof; empty disables it")
	queueCapacity := flag.Int("queue-capacity", 1024, "engine command queue capacity")
	subscriberBuffer := flag.Int("subscriber-buffer", grpcserver.DefaultSubscriberBuffer, "market-data updates buffered per subscriber")
	dataDirectory := flag.String("data-dir", "data", "directory for the event log and recovery snapshot")
	snapshotInterval := flag.Uint64("snapshot-interval", 1000, "accepted commands between recovery snapshots; zero disables snapshots")
	databaseURL := flag.String("database-url", "", "Postgres URL for asynchronous projections; empty disables projections")
	rebuildProjections := flag.Bool("rebuild-projections", false, "clear and rebuild Postgres projections from the event log before serving")
	projectionPoll := flag.Duration("projection-poll-interval", time.Second, "interval between projection catch-up attempts")
	projectionRetries := flag.Int("projection-retry-limit", 3, "retries per projection database operation")
	flag.Parse()

	if err := os.MkdirAll(*dataDirectory, 0o700); err != nil {
		log.Fatal(err)
	}
	matchingEngine, err := engine.NewPersistentEngine(*queueCapacity, engine.PersistenceConfig{
		LogPath:          filepath.Join(*dataDirectory, "events.log"),
		SnapshotPath:     filepath.Join(*dataDirectory, "snapshot.json"),
		SnapshotInterval: *snapshotInterval,
	})
	if err != nil {
		log.Fatal(err)
	}
	shutdownSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var projectionStore *projection.PostgresStore
	if *databaseURL != "" {
		projectionStore, err = projection.OpenPostgres(*databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer projectionStore.Close()
		projector, err := projection.New(projectionStore, projection.Config{
			EventLogPath: filepath.Join(*dataDirectory, "events.log"),
			PollInterval: *projectionPoll,
			RetryLimit:   *projectionRetries,
		})
		if err != nil {
			log.Fatal(err)
		}
		if *rebuildProjections {
			if err := projector.Rebuild(shutdownSignal); err != nil {
				log.Fatalf("rebuild projections: %v", err)
			}
		}
		go func() {
			if err := projector.Run(shutdownSignal); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("projection worker stopped: %v", err)
			}
		}()
	}
	service, err := grpcserver.New(matchingEngine, *subscriberBuffer)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	metrics := observability.New(matchingEngine)
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	if err := metrics.Register(registry); err != nil {
		log.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(metrics.UnaryServerInterceptor()))
	ordermatchv1.RegisterOrderMatchServiceServer(server, service)

	var observabilityServer *http.Server
	if *observabilityAddress != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
		mux.Handle("/debug/pprof/", http.DefaultServeMux)
		observabilityServer = &http.Server{Addr: *observabilityAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Printf("metrics and pprof listening on %s", *observabilityAddress)
			if err := observabilityServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("observability server stopped: %v", err)
			}
		}()
	}

	go func() {
		<-shutdownSignal.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := matchingEngine.Shutdown(ctx); err != nil {
			log.Printf("engine shutdown: %v", err)
		}
		if observabilityServer != nil {
			if err := observabilityServer.Shutdown(ctx); err != nil {
				log.Printf("observability shutdown: %v", err)
			}
		}
		forcedStop := time.AfterFunc(10*time.Second, server.Stop)
		defer forcedStop.Stop()
		server.GracefulStop()
	}()

	log.Printf("gRPC server listening on %s", *address)
	if err := server.Serve(listener); err != nil {
		log.Printf("gRPC server stopped: %v", err)
	}
}
