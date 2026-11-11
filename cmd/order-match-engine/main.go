package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	ordermatchv1 "github.com/lbraun7/order-match-engine/api/v1"
	"github.com/lbraun7/order-match-engine/internal/domain"
	"github.com/lbraun7/order-match-engine/internal/engine"
	"github.com/lbraun7/order-match-engine/internal/grpcserver"
	"github.com/lbraun7/order-match-engine/internal/observability"
	"github.com/lbraun7/order-match-engine/internal/projection"
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
	replicaDataDirectory := flag.String("replica-data-dir", "", "optional independent directory for a synchronous event-log replica")
	snapshotInterval := flag.Uint64("snapshot-interval", 1000, "accepted commands between recovery snapshots; zero disables snapshots")
	databaseURL := flag.String("database-url", "", "Postgres URL for asynchronous projections; empty disables projections")
	rebuildProjections := flag.Bool("rebuild-projections", false, "clear and rebuild Postgres projections from the event log before serving")
	projectionPoll := flag.Duration("projection-poll-interval", time.Second, "interval between projection catch-up attempts")
	projectionRetries := flag.Int("projection-retry-limit", 3, "retries per projection database operation")
	flag.Parse()

	if err := os.MkdirAll(*dataDirectory, 0o700); err != nil {
		log.Fatal(err)
	}
	if *replicaDataDirectory != "" {
		if filepath.Clean(*replicaDataDirectory) == filepath.Clean(*dataDirectory) {
			log.Fatal("replica-data-dir must differ from data-dir")
		}
		if err := os.MkdirAll(*replicaDataDirectory, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(*dataDirectory, "events.log")); err == nil {
		log.Fatal("legacy single-symbol event log found; move it and snapshot.json into the URL-safe base64 directory for its symbol before starting the multi-symbol service")
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	shutdownSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var projectionStore *projection.PostgresStore
	var err error
	if *databaseURL != "" {
		projectionStore, err = projection.OpenPostgres(*databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer projectionStore.Close()
	}
	router, err := engine.NewRouterWithFactory(func(symbol domain.Symbol) (*engine.Engine, error) {
		directory := filepath.Join(*dataDirectory, base64.RawURLEncoding.EncodeToString([]byte(symbol)))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		logPath := filepath.Join(directory, "events.log")
		var replicaLogPath string
		if *replicaDataDirectory != "" {
			replicaDirectory := filepath.Join(*replicaDataDirectory, base64.RawURLEncoding.EncodeToString([]byte(symbol)))
			if err := os.MkdirAll(replicaDirectory, 0o700); err != nil {
				return nil, err
			}
			replicaLogPath = filepath.Join(replicaDirectory, "events.log")
		}
		matchingEngine, err := engine.NewPersistentEngine(*queueCapacity, engine.PersistenceConfig{
			LogPath: logPath, SnapshotPath: filepath.Join(directory, "snapshot.json"),
			SnapshotInterval: *snapshotInterval, ReplicaLogPath: replicaLogPath,
		})
		if err != nil {
			return nil, err
		}
		if projectionStore != nil {
			projector, err := projection.New(projectionStore.ForSymbol(string(symbol)), projection.Config{
				EventLogPath: logPath, PollInterval: *projectionPoll, RetryLimit: *projectionRetries,
			})
			if err != nil {
				_ = matchingEngine.Shutdown(context.Background())
				return nil, err
			}
			if *rebuildProjections {
				if err := projector.Rebuild(shutdownSignal); err != nil {
					_ = matchingEngine.Shutdown(context.Background())
					return nil, fmt.Errorf("rebuild %s projections: %w", symbol, err)
				}
			}
			go func() {
				if err := projector.Run(shutdownSignal); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("projection worker for %s stopped: %v", symbol, err)
				}
			}()
		}
		return matchingEngine, nil
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, symbol := range persistedSymbols(*dataDirectory, *replicaDataDirectory) {
		if _, err := router.Snapshot(context.Background(), symbol); err != nil {
			log.Fatalf("restore symbol %s: %v", symbol, err)
		}
	}
	service, err := grpcserver.New(router, *subscriberBuffer)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	metrics := observability.New(router)
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
		if err := router.Shutdown(ctx); err != nil {
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

func persistedSymbols(dataDirectories ...string) []domain.Symbol {
	unique := make(map[domain.Symbol]bool)
	for _, dataDirectory := range dataDirectories {
		if dataDirectory == "" {
			continue
		}
		entries, err := os.ReadDir(dataDirectory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dataDirectory, entry.Name(), "events.log")); err != nil {
				continue
			}
			decoded, err := base64.RawURLEncoding.DecodeString(entry.Name())
			if err == nil && len(decoded) > 0 {
				unique[domain.Symbol(decoded)] = true
			}
		}
	}
	symbols := make([]domain.Symbol, 0, len(unique))
	for symbol := range unique {
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i] < symbols[j] })
	return symbols
}
