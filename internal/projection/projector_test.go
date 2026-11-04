package projection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/eventlog"
)

type memoryStore struct {
	checkpoint uint64
	events     map[uint64]Event
	failApply  int
	resets     int
}

func (s *memoryStore) Checkpoint(context.Context) (uint64, error) { return s.checkpoint, nil }
func (s *memoryStore) Apply(_ context.Context, event Event) error {
	if s.failApply > 0 {
		s.failApply--
		return errors.New("database unavailable")
	}
	if s.events == nil {
		s.events = make(map[uint64]Event)
	}
	if _, exists := s.events[event.EventSequence]; !exists {
		s.events[event.EventSequence] = event
	}
	if event.EventSequence > s.checkpoint {
		s.checkpoint = event.EventSequence
	}
	return nil
}
func (s *memoryStore) Reset(context.Context) error {
	s.checkpoint = 0
	s.events = make(map[uint64]Event)
	s.resets++
	return nil
}

func TestProjectorCatchesUpAfterTemporaryDatabaseFailure(t *testing.T) {
	path := createEventLog(t)
	store := &memoryStore{failApply: 2}
	projector, err := New(store, Config{
		EventLogPath: path,
		RetryLimit:   1,
		RetryBackoff: time.Microsecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := projector.Sync(context.Background()); err == nil {
		t.Fatal("first sync succeeded while database was unavailable")
	}
	if store.checkpoint != 0 {
		t.Fatalf("checkpoint = %d, want 0 after failed transaction", store.checkpoint)
	}
	if err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("catch-up sync: %v", err)
	}
	if store.checkpoint != 2 || len(store.events) != 2 {
		t.Fatalf("checkpoint/events = %d/%d, want 2/2", store.checkpoint, len(store.events))
	}
	second := store.events[2]
	if len(second.Trades) != 1 || len(second.Orders) != 2 {
		t.Fatalf("crossing event has %d trades and %d orders, want 1 and 2", len(second.Trades), len(second.Orders))
	}
	stats := projector.Stats()
	if stats.Retries != 1 || stats.AppliedEvents != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	if err := projector.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 2 {
		t.Fatalf("idempotent sync produced %d events", len(store.events))
	}
}

func TestProjectorRebuildResetsAndReplays(t *testing.T) {
	store := &memoryStore{checkpoint: 999, events: map[uint64]Event{999: {EventSequence: 999}}}
	projector, err := New(store, Config{EventLogPath: createEventLog(t), RetryLimit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.resets != 1 || store.checkpoint != 2 || len(store.events) != 2 {
		t.Fatalf("resets/checkpoint/events = %d/%d/%d", store.resets, store.checkpoint, len(store.events))
	}
}

func createEventLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	log, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commands := []struct {
		sequence uint64
		command  domain.PlaceOrder
	}{
		{1, domain.PlaceOrder{OrderID: "buy", Side: domain.SideBuy, OrderType: domain.OrderTypeLimit, TimeInForce: domain.TimeInForceGTC, Price: 100, Quantity: 5}},
		{2, domain.PlaceOrder{OrderID: "sell", Side: domain.SideSell, OrderType: domain.OrderTypeLimit, TimeInForce: domain.TimeInForceGTC, Price: 100, Quantity: 5}},
	}
	for _, item := range commands {
		payload, err := json.Marshal(item.command)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.Append(eventlog.Event{Sequence: item.sequence, Type: eventlog.TypePlace, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
