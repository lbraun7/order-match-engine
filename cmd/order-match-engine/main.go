package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"syscall"
	"time"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"github.com/lucasbraun96/order-match-engine/internal/grpcserver"
	"google.golang.org/grpc"
)

func main() {
	address := flag.String("address", ":50051", "gRPC listen address")
	queueCapacity := flag.Int("queue-capacity", 1024, "engine command queue capacity")
	subscriberBuffer := flag.Int("subscriber-buffer", grpcserver.DefaultSubscriberBuffer, "market-data updates buffered per subscriber")
	flag.Parse()

	matchingEngine, err := engine.NewEngine(*queueCapacity)
	if err != nil {
		log.Fatal(err)
	}
	service, err := grpcserver.New(matchingEngine, *subscriberBuffer)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	server := grpc.NewServer()
	ordermatchv1.RegisterOrderMatchServiceServer(server, service)

	shutdownSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownSignal.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := matchingEngine.Shutdown(ctx); err != nil {
			log.Printf("engine shutdown: %v", err)
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
