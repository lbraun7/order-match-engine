package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestPlaceOrderIsIdempotentAndRejectsConflictingReuse(t *testing.T) {
	client, cleanup := testClient(t, 8)
	defer cleanup()
	ctx := context.Background()
	req := limitOrder("client-1", "order-1", ordermatchv1.Side_SIDE_BUY, 100, 5)

	first, err := client.PlaceOrder(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.PlaceOrder(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != first.Sequence || second.Order.OrderId != "order-1" {
		t.Fatalf("unexpected idempotent responses: first=%v second=%v", first, second)
	}

	conflict := limitOrder("client-1", "order-2", ordermatchv1.Side_SIDE_BUY, 100, 5)
	if _, err := client.PlaceOrder(ctx, conflict); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %s, want AlreadyExists (%v)", status.Code(err), err)
	}
	book, err := client.GetBookSnapshot(ctx, &ordermatchv1.GetBookSnapshotRequest{})
	if err != nil || len(book.Snapshot.Bids) != 1 || len(book.Snapshot.Bids[0].Orders) != 1 {
		t.Fatalf("duplicate changed book: snapshot=%v err=%v", book, err)
	}
}

func TestCRUDAndDomainErrorMapping(t *testing.T) {
	client, cleanup := testClient(t, 8)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.PlaceOrder(ctx, limitOrder("bad", "bad", ordermatchv1.Side_SIDE_BUY, 0, 1)); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid price code = %s", status.Code(err))
	}
	placed, err := client.PlaceOrder(ctx, limitOrder("client", "order", ordermatchv1.Side_SIDE_SELL, 101, 3))
	if err != nil || placed.Order.Status != ordermatchv1.OrderStatus_ORDER_STATUS_OPEN {
		t.Fatalf("place: response=%v err=%v", placed, err)
	}
	got, err := client.GetOrder(ctx, &ordermatchv1.GetOrderRequest{OrderId: "order"})
	if err != nil || got.Order.OrderId != "order" {
		t.Fatalf("get: response=%v err=%v", got, err)
	}
	canceled, err := client.CancelOrder(ctx, &ordermatchv1.CancelOrderRequest{OrderId: "order"})
	if err != nil || canceled.Sequence != 2 || canceled.Order.Status != ordermatchv1.OrderStatus_ORDER_STATUS_CANCELED {
		t.Fatalf("cancel: response=%v err=%v", canceled, err)
	}
	if _, err := client.CancelOrder(ctx, &ordermatchv1.CancelOrderRequest{OrderId: "order"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second cancel code = %s", status.Code(err))
	}
	if _, err := client.GetOrder(ctx, &ordermatchv1.GetOrderRequest{OrderId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing code = %s", status.Code(err))
	}
}

func TestMarketDataStartsWithSnapshotThenSendsSequencedDeltas(t *testing.T) {
	client, cleanup := testClient(t, 8)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.StreamMarketData(ctx, &ordermatchv1.StreamMarketDataRequest{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := stream.Recv()
	if err != nil || initial.GetSnapshot() == nil || initial.Sequence != 0 {
		t.Fatalf("initial update=%v err=%v", initial, err)
	}
	if _, err := client.PlaceOrder(ctx, limitOrder("stream-client", "stream-order", ordermatchv1.Side_SIDE_BUY, 99, 2)); err != nil {
		t.Fatal(err)
	}
	delta, err := stream.Recv()
	if err != nil || delta.GetDelta() == nil || delta.Sequence != 1 || delta.PreviousSequence != 0 || delta.GetDelta().Order.OrderId != "stream-order" {
		t.Fatalf("delta=%v err=%v", delta, err)
	}
}

func TestBrokerDisconnectsSlowSubscriberWithoutDroppingIndividualUpdates(t *testing.T) {
	b := newBroker(1)
	sub := b.subscribe()
	b.publish(&ordermatchv1.MarketDataUpdate{Sequence: 1})
	b.publish(&ordermatchv1.MarketDataUpdate{Sequence: 2})
	first, ok := <-sub.updates
	if !ok || first.Sequence != 1 {
		t.Fatalf("first=%v ok=%v", first, ok)
	}
	if _, ok := <-sub.updates; ok {
		t.Fatal("slow subscriber channel remains open")
	}
}

func limitOrder(clientID, orderID string, side ordermatchv1.Side, price, quantity int64) *ordermatchv1.PlaceOrderRequest {
	return &ordermatchv1.PlaceOrderRequest{ClientOrderId: clientID, OrderId: orderID, Side: side, OrderType: ordermatchv1.OrderType_ORDER_TYPE_LIMIT, TimeInForce: ordermatchv1.TimeInForce_TIME_IN_FORCE_GTC, Price: price, Quantity: quantity}
}

func testClient(t *testing.T, subscriberBuffer int) (ordermatchv1.OrderMatchServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	matchingEngine, err := engine.NewEngine(64)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(matchingEngine, subscriberBuffer)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	ordermatchv1.RegisterOrderMatchServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = connection.Close()
		server.Stop()
		_ = matchingEngine.Shutdown(context.Background())
	}
	return ordermatchv1.NewOrderMatchServiceClient(connection), cleanup
}
