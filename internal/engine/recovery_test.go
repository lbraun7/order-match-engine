package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

func TestPersistentEngineRestoresSnapshotAndReplaysLogRemainder(t *testing.T) {
	directory := t.TempDir()
	config := PersistenceConfig{
		LogPath:          filepath.Join(directory, "events.log"),
		SnapshotPath:     filepath.Join(directory, "snapshot.json"),
		SnapshotInterval: 250,
	}
	engine, err := NewPersistentEngine(16, config)
	if err != nil {
		t.Fatal(err)
	}

	const commands = 3001
	for i := 0; i < commands; i++ {
		id := domain.OrderID(fmt.Sprintf("order-%04d", i))
		side := domain.SideBuy
		price := domain.Price(99)
		if i%2 == 1 {
			side = domain.SideSell
			price = 101
		}
		if _, err := engine.PlaceOrder(context.Background(), command(id, side, price, 1)); err != nil {
			t.Fatalf("place %d: %v", i, err)
		}
		if i%5 == 0 {
			if _, err := engine.CancelOrder(context.Background(), domain.CancelOrder{OrderID: id}); err != nil {
				t.Fatalf("cancel %d: %v", i, err)
			}
		}
	}
	want, err := engine.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	restored, err := NewPersistentEngine(16, config)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownEngine(t, restored)
	got, err := restored.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored snapshot differs: got sequence %d, want %d", got.Sequence, want.Sequence)
	}
	for _, id := range []domain.OrderID{"order-0000", "order-1333", "order-3000"} {
		gotOrder, err := restored.GetOrder(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if gotOrder.ID != id {
			t.Fatalf("restored order ID = %s, want %s", gotOrder.ID, id)
		}
	}
}
