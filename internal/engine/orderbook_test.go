package engine

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lbraun7/order-match-engine/internal/domain"
)

func TestOrderBookEmpty(t *testing.T) {
	book := NewOrderBook()
	if len(book.bids.prices) != 0 || len(book.asks.prices) != 0 || len(book.active) != 0 {
		t.Fatal("new book is not empty")
	}
}

func TestOrderWithoutCounterpartyRestsAtSortedPrice(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "b1", domain.SideBuy, 100, 2)
	place(t, book, "b2", domain.SideBuy, 102, 3)
	place(t, book, "b3", domain.SideBuy, 101, 4)
	place(t, book, "a1", domain.SideSell, 105, 2)
	place(t, book, "a2", domain.SideSell, 103, 3)

	if want := []domain.Price{102, 101, 100}; !reflect.DeepEqual(book.bids.prices, want) {
		t.Fatalf("bid prices = %v, want %v", book.bids.prices, want)
	}
	if want := []domain.Price{103, 105}; !reflect.DeepEqual(book.asks.prices, want) {
		t.Fatalf("ask prices = %v, want %v", book.asks.prices, want)
	}
}

func TestTotalAndPartialMatchAtMakerPrice(t *testing.T) {
	book := NewOrderBook()
	maker, _ := place(t, book, "ask", domain.SideSell, 100, 5)
	taker, trades := place(t, book, "bid", domain.SideBuy, 110, 3)

	if len(trades) != 1 || trades[0].Price() != 100 || trades[0].Quantity() != 3 {
		t.Fatalf("unexpected trades: %+v", trades)
	}
	if maker.RemainingQuantity() != 2 || maker.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatalf("maker remaining/status = %d/%v", maker.RemainingQuantity(), maker.Status())
	}
	if taker.RemainingQuantity() != 0 || taker.Status() != domain.OrderStatusFilled {
		t.Fatalf("taker remaining/status = %d/%v", taker.RemainingQuantity(), taker.Status())
	}
	if _, active := book.active[taker.ID()]; active {
		t.Fatal("filled taker remained active")
	}
}

func TestPricePriorityAndMultipleLevels(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "expensive", domain.SideSell, 102, 2)
	place(t, book, "best", domain.SideSell, 100, 2)
	place(t, book, "middle", domain.SideSell, 101, 2)
	_, trades := place(t, book, "buyer", domain.SideBuy, 102, 5)

	assertTradeMakers(t, trades, []domain.OrderID{"best", "middle", "expensive"})
	if trades[2].Quantity() != 1 {
		t.Fatalf("last trade quantity = %d, want 1", trades[2].Quantity())
	}
	if want := []domain.Price{102}; !reflect.DeepEqual(book.asks.prices, want) {
		t.Fatalf("ask prices = %v, want %v", book.asks.prices, want)
	}
}

func TestFIFOAndMultipleOrdersAtLevel(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "first", domain.SideSell, 100, 2)
	place(t, book, "second", domain.SideSell, 100, 2)
	place(t, book, "third", domain.SideSell, 100, 2)
	_, trades := place(t, book, "buyer", domain.SideBuy, 100, 5)

	assertTradeMakers(t, trades, []domain.OrderID{"first", "second", "third"})
	if book.asks.levels[100].Len() != 1 || book.asks.levels[100].Front().Value.(*domain.Order).ID() != "third" {
		t.Fatal("FIFO level did not retain only the partially filled third order")
	}
}

func TestBookDoesNotRemainCrossedAndConservesQuantity(t *testing.T) {
	book := NewOrderBook()
	makers := []*domain.Order{}
	for _, input := range []struct {
		id       domain.OrderID
		price    domain.Price
		quantity domain.Quantity
	}{{"a1", 99, 2}, {"a2", 100, 3}, {"a3", 101, 4}} {
		order, _ := place(t, book, input.id, domain.SideSell, input.price, input.quantity)
		makers = append(makers, order)
	}
	taker, trades := place(t, book, "buyer", domain.SideBuy, 100, 8)

	var traded domain.Quantity
	for _, trade := range trades {
		traded += trade.Quantity()
	}
	if traded != 5 || taker.ExecutedQuantity() != traded {
		t.Fatalf("traded/taker executed = %d/%d, want 5/5", traded, taker.ExecutedQuantity())
	}
	var makerExecuted domain.Quantity
	for _, maker := range makers {
		makerExecuted += maker.ExecutedQuantity()
	}
	if makerExecuted != traded {
		t.Fatalf("maker executed = %d, traded = %d", makerExecuted, traded)
	}
	if len(book.bids.prices) > 0 && len(book.asks.prices) > 0 && book.bids.prices[0] >= book.asks.prices[0] {
		t.Fatalf("crossed book: best bid %d, best ask %d", book.bids.prices[0], book.asks.prices[0])
	}
}

func TestTradeIDsAndSequencesAreDeterministic(t *testing.T) {
	run := func() []domain.Trade {
		book := NewOrderBook()
		place(t, book, "a1", domain.SideSell, 100, 1)
		place(t, book, "a2", domain.SideSell, 100, 1)
		_, trades := place(t, book, "buyer", domain.SideBuy, 100, 2)
		return trades
	}
	first, second := run(), run()
	for i := range first {
		if first[i].ID() != second[i].ID() || first[i].Sequence() != second[i].Sequence() {
			t.Fatalf("run differs at trade %d", i)
		}
	}
	if first[0].ID() != "4" || first[0].Sequence() != 4 || first[1].ID() != "5" || first[1].Sequence() != 5 {
		t.Fatalf("unexpected IDs/sequences: %s/%d, %s/%d", first[0].ID(), first[0].Sequence(), first[1].ID(), first[1].Sequence())
	}
}

func TestRejectedOrderDoesNotConsumeSequence(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "one", domain.SideBuy, 100, 1)
	_, _, err := book.PlaceOrder(command("one", domain.SideBuy, 100, 1))
	if !errors.Is(err, ErrDuplicateOrderID) {
		t.Fatalf("error = %v, want duplicate", err)
	}
	order, _ := place(t, book, "two", domain.SideBuy, 99, 1)
	if order.Sequence() != 2 {
		t.Fatalf("sequence = %d, want 2", order.Sequence())
	}
}

func TestCancelOrderAtBeginningMiddleAndEndPreservesFIFO(t *testing.T) {
	for _, canceled := range []domain.OrderID{"first", "middle", "last"} {
		t.Run(string(canceled), func(t *testing.T) {
			book := NewOrderBook()
			for _, id := range []domain.OrderID{"first", "middle", "last"} {
				place(t, book, id, domain.SideSell, 100, 2)
			}
			got, err := book.CancelOrder(domain.CancelOrder{OrderID: canceled})
			if err != nil || got.Status != domain.OrderStatusCanceled || got.RemainingQuantity != 2 {
				t.Fatalf("cancel = %+v, %v", got, err)
			}

			_, trades := place(t, book, "buyer", domain.SideBuy, 100, 4)
			want := make([]domain.OrderID, 0, 2)
			for _, id := range []domain.OrderID{"first", "middle", "last"} {
				if id != canceled {
					want = append(want, id)
				}
			}
			assertTradeMakers(t, trades, want)
			if _, active := book.active[canceled]; active {
				t.Fatal("canceled order remained in active index")
			}
		})
	}
}

func TestCancelRemovesEmptyPriceLevel(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "best", domain.SideBuy, 101, 1)
	place(t, book, "next", domain.SideBuy, 100, 1)
	if _, err := book.CancelOrder(domain.CancelOrder{OrderID: "best"}); err != nil {
		t.Fatal(err)
	}
	if want := []domain.Price{100}; !reflect.DeepEqual(book.bids.prices, want) {
		t.Fatalf("bid prices = %v, want %v", book.bids.prices, want)
	}
	if _, exists := book.bids.levels[101]; exists {
		t.Fatal("empty price level remained indexed")
	}
}

func TestCancelOrderErrors(t *testing.T) {
	book := NewOrderBook()
	if _, err := book.CancelOrder(domain.CancelOrder{OrderID: "missing"}); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := book.CancelOrder(domain.CancelOrder{}); !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("invalid error = %v", err)
	}

	place(t, book, "cancel", domain.SideBuy, 90, 1)
	if _, err := book.CancelOrder(domain.CancelOrder{OrderID: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := book.CancelOrder(domain.CancelOrder{OrderID: "cancel"}); !errors.Is(err, domain.ErrOrderNotOpen) {
		t.Fatalf("second cancel error = %v", err)
	}

	place(t, book, "maker", domain.SideSell, 100, 1)
	place(t, book, "taker", domain.SideBuy, 100, 1)
	if _, err := book.CancelOrder(domain.CancelOrder{OrderID: "maker"}); !errors.Is(err, domain.ErrOrderNotOpen) {
		t.Fatalf("filled cancel error = %v", err)
	}
}

func TestGetOrderAndSnapshot(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "b1", domain.SideBuy, 100, 2)
	place(t, book, "b2", domain.SideBuy, 100, 3)
	place(t, book, "b3", domain.SideBuy, 99, 4)
	place(t, book, "a1", domain.SideSell, 105, 5)

	order, err := book.GetOrder("b2")
	if err != nil || order.ID != "b2" || order.RemainingQuantity != 3 || order.Status != domain.OrderStatusOpen {
		t.Fatalf("order = %+v, %v", order, err)
	}
	if _, err := book.GetOrder("unknown"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("unknown order error = %v", err)
	}

	snapshot := book.Snapshot()
	if snapshot.Sequence != 4 || len(snapshot.Bids) != 2 || len(snapshot.Asks) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Bids[0].Price != 100 || snapshot.Bids[0].TotalQuantity != 5 {
		t.Fatalf("best bid = %+v", snapshot.Bids[0])
	}
	if got := []domain.OrderID{snapshot.Bids[0].Orders[0].ID, snapshot.Bids[0].Orders[1].ID}; !reflect.DeepEqual(got, []domain.OrderID{"b1", "b2"}) {
		t.Fatalf("FIFO order = %v", got)
	}

	// Returned slices are independent of the live book.
	snapshot.Bids[0].Orders[0].RemainingQuantity = 999
	fresh, _ := book.GetOrder("b1")
	if fresh.RemainingQuantity != 2 {
		t.Fatal("snapshot mutation changed live order")
	}
}

func place(t *testing.T, book *OrderBook, id domain.OrderID, side domain.Side, price domain.Price, quantity domain.Quantity) (*domain.Order, []domain.Trade) {
	t.Helper()
	order, trades, err := book.PlaceOrder(command(id, side, price, quantity))
	if err != nil {
		t.Fatalf("place %s: %v", id, err)
	}
	return order, trades
}

func command(id domain.OrderID, side domain.Side, price domain.Price, quantity domain.Quantity) domain.PlaceOrder {
	return domain.PlaceOrder{OrderID: id, Side: side, OrderType: domain.OrderTypeLimit, TimeInForce: domain.TimeInForceGTC, Price: price, Quantity: quantity}
}

func assertTradeMakers(t *testing.T, trades []domain.Trade, want []domain.OrderID) {
	t.Helper()
	got := make([]domain.OrderID, len(trades))
	for i, trade := range trades {
		got[i] = trade.MakerOrderID()
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trade makers = %v, want %v", got, want)
	}
}
