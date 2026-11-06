// Command demo runs a small end-to-end scenario against the public gRPC API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type demoResult struct {
	Symbol        string                           `json:"symbol"`
	RestingOrder  *ordermatchv1.PlaceOrderResponse `json:"resting_order"`
	Aggressor     *ordermatchv1.PlaceOrderResponse `json:"aggressor"`
	FinalSnapshot *ordermatchv1.BookSnapshot       `json:"final_snapshot"`
}

func main() {
	target := flag.String("target", "localhost:50051", "gRPC server address")
	runID := flag.String("run-id", fmt.Sprintf("demo-%d", time.Now().UnixNano()), "unique ID prefix")
	symbol := flag.String("symbol", "", "symbol to use; defaults to a unique DEMO symbol")
	timeout := flag.Duration("timeout", 30*time.Second, "total time allowed for the demo")
	flag.Parse()
	if *symbol == "" {
		*symbol = "DEMO-" + *runID
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	connection, err := grpc.NewClient(*target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fail(err)
	}
	defer connection.Close()
	client := ordermatchv1.NewOrderMatchServiceClient(connection)
	if err := waitUntilReady(ctx, client, *symbol); err != nil {
		fail(err)
	}

	resting, err := client.PlaceOrder(ctx, limitOrder(*runID+"-ask", *symbol, ordermatchv1.Side_SIDE_SELL, 10100, 5))
	if err != nil {
		fail(fmt.Errorf("place resting ask: %w", err))
	}
	aggressor, err := client.PlaceOrder(ctx, limitOrder(*runID+"-bid", *symbol, ordermatchv1.Side_SIDE_BUY, 10200, 3))
	if err != nil {
		fail(fmt.Errorf("place aggressive bid: %w", err))
	}
	snapshot, err := client.GetBookSnapshot(ctx, &ordermatchv1.GetBookSnapshotRequest{Symbol: *symbol})
	if err != nil {
		fail(fmt.Errorf("get final snapshot: %w", err))
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(demoResult{
		Symbol: *symbol, RestingOrder: resting, Aggressor: aggressor, FinalSnapshot: snapshot.Snapshot,
	}); err != nil {
		fail(err)
	}
}

func waitUntilReady(ctx context.Context, client ordermatchv1.OrderMatchServiceClient, symbol string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := client.GetBookSnapshot(callCtx, &ordermatchv1.GetBookSnapshotRequest{Symbol: symbol})
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for service: %w (last RPC error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func limitOrder(id, symbol string, side ordermatchv1.Side, price, quantity int64) *ordermatchv1.PlaceOrderRequest {
	return &ordermatchv1.PlaceOrderRequest{
		ClientOrderId: id, OrderId: id, Symbol: symbol, Side: side,
		OrderType:   ordermatchv1.OrderType_ORDER_TYPE_LIMIT,
		TimeInForce: ordermatchv1.TimeInForce_TIME_IN_FORCE_GTC,
		Price:       price, Quantity: quantity,
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
