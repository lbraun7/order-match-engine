package projection

import (
	"context"
	"os"
	"testing"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"github.com/lucasbraun96/order-match-engine/internal/eventlog"
)

func TestPostgresStoreAppliesEventAtomicallyAndIdempotently(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	store, err := OpenPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	trade, err := domain.NewTrade("2", 2, "maker", "taker", 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		EventSequence: 1,
		EventType:     eventlog.TypePlace,
		Orders: []engine.OrderSnapshot{{
			ID: "taker", Sequence: 1, Side: domain.SideBuy, Type: domain.OrderTypeLimit,
			TimeInForce: domain.TimeInForceGTC, Price: 100, OriginalQuantity: 3,
			ExecutedQuantity: 3, Status: domain.OrderStatusFilled,
		}},
		Trades: []domain.Trade{trade},
	}
	if err := store.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, event); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	checkpoint, err := store.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != 1 {
		t.Fatalf("checkpoint = %d, want 1", checkpoint)
	}
	for table, want := range map[string]int{"projection_events": 1, "orders": 1, "order_history": 1, "trades": 1} {
		var got int
		if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}
