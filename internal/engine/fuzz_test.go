package engine

import (
	"reflect"
	"testing"

	"github.com/lbraun7/order-match-engine/internal/domain"
)

func FuzzCommandSequence(f *testing.F) {
	f.Add([]byte{0, 0, 100, 5, 0, 0, 1, 100, 3, 1})
	f.Add([]byte{0, 0, 100, 2, 0, 0, 2, 100, 2, 0, 1, 3, 100, 4, 0})
	f.Add([]byte{0, 0, 101, 1, 0, 1, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2_000 {
			t.Skip()
		}
		first := runCommands(data)
		second := runCommands(data)
		if !reflect.DeepEqual(first.Snapshot(), second.Snapshot()) {
			t.Fatal("same command bytes produced different snapshots")
		}
		assertBookInvariants(t, first)
	})
}

func FuzzRestoreOrderBook(f *testing.F) {
	book := NewOrderBook()
	_, _, _ = book.PlaceOrder(command("seed", domain.SideBuy, 100, 2))
	seed, err := book.marshalState()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"sequence":1,"orders":[]}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		restored, err := restoreOrderBook(data)
		if err != nil {
			return
		}
		assertBookInvariants(t, restored)
		roundTrip, err := restored.marshalState()
		if err != nil {
			t.Fatal(err)
		}
		again, err := restoreOrderBook(roundTrip)
		if err != nil {
			t.Fatalf("restore canonical state: %v", err)
		}
		if !reflect.DeepEqual(restored.Snapshot(), again.Snapshot()) {
			t.Fatal("snapshot changed after canonical round trip")
		}
	})
}

func runCommands(data []byte) *OrderBook {
	book := NewOrderBook()
	for offset := 0; offset+4 < len(data); offset += 5 {
		opcode, slot := data[offset], data[offset+1]%32
		id := domain.OrderID([]byte{'o', '0' + slot/10, '0' + slot%10})
		operation := opcode % 9
		if operation == 1 {
			_, _ = book.CancelOrder(domain.CancelOrder{OrderID: id})
			continue
		}
		if operation == 8 {
			participant := domain.ParticipantID("")
			if slot%2 == 0 {
				participant = domain.ParticipantID([]byte{'p', '0' + slot%4})
			}
			_, _ = book.MassCancel(domain.MassCancel{ParticipantID: participant})
			continue
		}
		side := domain.SideBuy
		if data[offset+4]&1 == 1 {
			side = domain.SideSell
		}
		price := domain.Price(data[offset+2]%20 + 1)
		quantity := domain.Quantity(data[offset+3]%10 + 1)
		place := advancedLimit(id, side, price, quantity, domain.TimeInForceGTC)
		place.ParticipantID = domain.ParticipantID([]byte{'p', '0' + slot%4})
		place.SelfTradePrevention = domain.SelfTradePrevention(data[offset+4] % 4)
		switch operation {
		case 2:
			place.TimeInForce = domain.TimeInForceIOC
		case 3:
			place.TimeInForce = domain.TimeInForceFOK
		case 4:
			place.OrderType = domain.OrderTypeMarket
			place.TimeInForce = domain.TimeInForceIOC
			place.Price = 0
		case 5:
			place.OrderType = domain.OrderTypeIceberg
			if place.Quantity == 1 {
				place.Quantity = 2
			}
			place.DisplayQuantity = max(1, place.Quantity/2)
		case 6:
			place.OrderType = domain.OrderTypeStopMarket
			place.Price = 0
			place.StopPrice = price
		case 7:
			place.OrderType = domain.OrderTypeStopLimit
			place.StopPrice = price
		}
		_, _, _ = book.PlaceOrder(place)
	}
	return book
}

func assertBookInvariants(t *testing.T, book *OrderBook) {
	t.Helper()
	snapshot := book.Snapshot()
	if len(snapshot.Bids) > 0 && len(snapshot.Asks) > 0 && snapshot.Bids[0].Price >= snapshot.Asks[0].Price {
		t.Fatalf("crossed book: bid %d, ask %d", snapshot.Bids[0].Price, snapshot.Asks[0].Price)
	}
	seenActive := make(map[domain.OrderID]bool)
	checkSide := func(levels []PriceLevelSnapshot, descending bool) {
		var previous domain.Price
		for index, level := range levels {
			if index > 0 && ((descending && previous <= level.Price) || (!descending && previous >= level.Price)) {
				t.Fatalf("price levels out of order: %d then %d", previous, level.Price)
			}
			previous = level.Price
			var total domain.Quantity
			var previousSequence domain.Sequence
			var previousWasRegular bool
			for _, order := range level.Orders {
				if order.Price != level.Price || order.RemainingQuantity <= 0 {
					t.Fatalf("invalid resting order %+v at level %d", order, level.Price)
				}
				if order.OriginalQuantity != order.RemainingQuantity+order.ExecutedQuantity {
					t.Fatalf("quantity not conserved for %s", order.ID)
				}
				if previousWasRegular && order.Type == domain.OrderTypeLimit && previousSequence >= order.Sequence {
					t.Fatalf("non-FIFO sequence at price %d", level.Price)
				}
				previousSequence = order.Sequence
				previousWasRegular = order.Type == domain.OrderTypeLimit
				total += order.VisibleQuantity
				seenActive[order.ID] = true
			}
			if total != level.TotalQuantity {
				t.Fatalf("level %d total = %d, want %d", level.Price, level.TotalQuantity, total)
			}
		}
	}
	checkSide(snapshot.Bids, true)
	checkSide(snapshot.Asks, false)
	if len(seenActive) != len(book.active) {
		t.Fatalf("snapshot has %d active orders, index has %d", len(seenActive), len(book.active))
	}
	for id, location := range book.active {
		if !seenActive[id] || location.element == nil || location.element.Value.(*domain.Order).ID() != id {
			t.Fatalf("invalid active index entry for %s", id)
		}
	}
	for id, order := range book.orders {
		if order.OriginalQuantity() != order.RemainingQuantity()+order.ExecutedQuantity() {
			t.Fatalf("quantity not conserved for stored order %s", id)
		}
		current := book.snapshotOrder(order)
		active := (order.Status() == domain.OrderStatusOpen || order.Status() == domain.OrderStatusPartiallyFilled) && !current.Dormant
		if active != seenActive[id] {
			t.Fatalf("order %s active status/index mismatch", id)
		}
	}
}
