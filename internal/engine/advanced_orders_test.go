package engine

import (
	"testing"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

func TestMarketOrderUsesIOCAndMakerPrice(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "ask", domain.SideSell, 101, 2)
	result, err := book.PlaceOrderDetailed(domain.PlaceOrder{
		OrderID: "market", Side: domain.SideBuy, OrderType: domain.OrderTypeMarket,
		TimeInForce: domain.TimeInForceIOC, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 1 || result.Trades[0].Price() != 101 || result.Trades[0].Quantity() != 2 {
		t.Fatalf("trades = %+v", result.Trades)
	}
	if result.Order.Status() != domain.OrderStatusCanceled || result.Order.RemainingQuantity() != 1 {
		t.Fatalf("market remainder = %d status = %v", result.Order.RemainingQuantity(), result.Order.Status())
	}
	if len(book.Snapshot().Bids) != 0 {
		t.Fatal("market remainder rested")
	}
}

func TestIOCAndFOKSemantics(t *testing.T) {
	t.Run("IOC cancels remainder", func(t *testing.T) {
		book := NewOrderBook()
		place(t, book, "ask", domain.SideSell, 100, 2)
		result, err := book.PlaceOrderDetailed(advancedLimit("ioc", domain.SideBuy, 100, 5, domain.TimeInForceIOC))
		if err != nil {
			t.Fatal(err)
		}
		if result.Order.ExecutedQuantity() != 2 || result.Order.RemainingQuantity() != 3 || result.Order.Status() != domain.OrderStatusCanceled {
			t.Fatalf("IOC = %+v", snapshotOrder(result.Order))
		}
	})

	t.Run("FOK is atomic when liquidity is insufficient", func(t *testing.T) {
		book := NewOrderBook()
		place(t, book, "ask", domain.SideSell, 100, 2)
		result, err := book.PlaceOrderDetailed(advancedLimit("fok", domain.SideBuy, 100, 3, domain.TimeInForceFOK))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Trades) != 0 || result.Order.Status() != domain.OrderStatusCanceled {
			t.Fatalf("FOK result = %+v", result)
		}
		maker, _ := book.GetOrder("ask")
		if maker.RemainingQuantity != 2 {
			t.Fatalf("maker remaining = %d", maker.RemainingQuantity)
		}
	})

	t.Run("FOK fills completely", func(t *testing.T) {
		book := NewOrderBook()
		place(t, book, "a1", domain.SideSell, 100, 1)
		place(t, book, "a2", domain.SideSell, 101, 2)
		result, err := book.PlaceOrderDetailed(advancedLimit("fok", domain.SideBuy, 101, 3, domain.TimeInForceFOK))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Trades) != 2 || result.Order.Status() != domain.OrderStatusFilled {
			t.Fatalf("FOK result = %+v", result)
		}
	})

	t.Run("FOK counts hidden iceberg liquidity", func(t *testing.T) {
		book := NewOrderBook()
		iceberg := advancedLimit("ice", domain.SideSell, 100, 5, domain.TimeInForceGTC)
		iceberg.OrderType = domain.OrderTypeIceberg
		iceberg.DisplayQuantity = 1
		if _, err := book.PlaceOrderDetailed(iceberg); err != nil {
			t.Fatal(err)
		}
		result, err := book.PlaceOrderDetailed(advancedLimit("fok", domain.SideBuy, 100, 5, domain.TimeInForceFOK))
		if err != nil {
			t.Fatal(err)
		}
		if result.Order.Status() != domain.OrderStatusFilled || len(result.Trades) != 5 {
			t.Fatalf("FOK against iceberg = %+v", result)
		}
	})
}

func TestSelfTradePreventionModes(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        domain.SelfTradePrevention
		makerStatus domain.OrderStatus
		takerStatus domain.OrderStatus
		trades      int
	}{
		{"cancel aggressor", domain.SelfTradePreventionCancelAggressor, domain.OrderStatusOpen, domain.OrderStatusCanceled, 0},
		{"cancel resting", domain.SelfTradePreventionCancelResting, domain.OrderStatusCanceled, domain.OrderStatusOpen, 0},
		{"cancel both", domain.SelfTradePreventionCancelBoth, domain.OrderStatusCanceled, domain.OrderStatusCanceled, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			book := NewOrderBook()
			maker := advancedLimit("maker", domain.SideSell, 100, 1, domain.TimeInForceGTC)
			maker.ParticipantID = "same"
			if _, err := book.PlaceOrderDetailed(maker); err != nil {
				t.Fatal(err)
			}
			taker := advancedLimit("taker", domain.SideBuy, 100, 1, domain.TimeInForceGTC)
			taker.ParticipantID = "same"
			taker.SelfTradePrevention = test.mode
			result, err := book.PlaceOrderDetailed(taker)
			if err != nil {
				t.Fatal(err)
			}
			gotMaker, _ := book.GetOrder("maker")
			if gotMaker.Status != test.makerStatus || result.Order.Status() != test.takerStatus || len(result.Trades) != test.trades {
				t.Fatalf("maker=%v taker=%v trades=%d", gotMaker.Status, result.Order.Status(), len(result.Trades))
			}
		})
	}
}

func TestIcebergReplenishesAtBackOfFIFO(t *testing.T) {
	book := NewOrderBook()
	iceberg := advancedLimit("ice", domain.SideSell, 100, 5, domain.TimeInForceGTC)
	iceberg.OrderType = domain.OrderTypeIceberg
	iceberg.DisplayQuantity = 2
	if _, err := book.PlaceOrderDetailed(iceberg); err != nil {
		t.Fatal(err)
	}
	place(t, book, "regular", domain.SideSell, 100, 2)
	result, err := book.PlaceOrderDetailed(advancedLimit("buyer", domain.SideBuy, 100, 3, domain.TimeInForceGTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 2 || result.Trades[0].MakerOrderID() != "ice" || result.Trades[1].MakerOrderID() != "regular" {
		t.Fatalf("trade makers = %+v", result.Trades)
	}
	ice, _ := book.GetOrder("ice")
	if ice.RemainingQuantity != 3 || ice.VisibleQuantity != 2 {
		t.Fatalf("iceberg = %+v", ice)
	}
	level := book.Snapshot().Asks[0]
	if level.TotalQuantity != 3 || level.Orders[0].ID != "regular" || level.Orders[1].ID != "ice" {
		t.Fatalf("level = %+v", level)
	}
}

func TestStopMarketTriggersFromTrade(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "at-trigger", domain.SideSell, 105, 1)
	place(t, book, "after-trigger", domain.SideSell, 106, 1)
	stop := domain.PlaceOrder{
		OrderID: "stop", Side: domain.SideBuy, OrderType: domain.OrderTypeStopMarket,
		TimeInForce: domain.TimeInForceGTC, StopPrice: 105, Quantity: 1,
	}
	if _, err := book.PlaceOrderDetailed(stop); err != nil {
		t.Fatal(err)
	}
	before, _ := book.GetOrder("stop")
	if !before.Dormant {
		t.Fatal("stop order was not dormant")
	}
	result, err := book.PlaceOrderDetailed(advancedLimit("trigger", domain.SideBuy, 105, 1, domain.TimeInForceGTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 2 || result.Trades[0].MakerOrderID() != "at-trigger" || result.Trades[1].TakerOrderID() != "stop" || result.Trades[1].Price() != 106 {
		t.Fatalf("trades = %+v", result.Trades)
	}
	after, _ := book.GetOrder("stop")
	if after.Dormant || after.Status != domain.OrderStatusFilled {
		t.Fatalf("stop after trigger = %+v", after)
	}
}

func TestStopAlreadyThroughMarketActivatesImmediately(t *testing.T) {
	book := NewOrderBook()
	place(t, book, "trade-maker", domain.SideSell, 100, 1)
	if _, err := book.PlaceOrderDetailed(advancedLimit("trade-taker", domain.SideBuy, 100, 1, domain.TimeInForceGTC)); err != nil {
		t.Fatal(err)
	}
	place(t, book, "liquidity", domain.SideSell, 101, 1)
	result, err := book.PlaceOrderDetailed(domain.PlaceOrder{
		OrderID: "late-stop", Side: domain.SideBuy, OrderType: domain.OrderTypeStopMarket,
		TimeInForce: domain.TimeInForceGTC, StopPrice: 99, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 1 || result.Trades[0].TakerOrderID() != "late-stop" || result.Order.Status() != domain.OrderStatusFilled {
		t.Fatalf("immediate stop = %+v", result)
	}
}

func TestMassCancelByParticipantIncludesDormantStops(t *testing.T) {
	book := NewOrderBook()
	one := advancedLimit("one", domain.SideBuy, 99, 1, domain.TimeInForceGTC)
	one.ParticipantID = "p1"
	two := advancedLimit("two", domain.SideBuy, 98, 1, domain.TimeInForceGTC)
	two.ParticipantID = "p2"
	stop := domain.PlaceOrder{OrderID: "stop", ParticipantID: "p1", Side: domain.SideSell, OrderType: domain.OrderTypeStopLimit, TimeInForce: domain.TimeInForceGTC, Price: 90, StopPrice: 95, Quantity: 1}
	for _, command := range []domain.PlaceOrder{one, two, stop} {
		if _, err := book.PlaceOrderDetailed(command); err != nil {
			t.Fatal(err)
		}
	}
	canceled, err := book.MassCancel(domain.MassCancel{ParticipantID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 2 || canceled[0].ID != "one" || canceled[1].ID != "stop" {
		t.Fatalf("canceled = %+v", canceled)
	}
	remaining, _ := book.GetOrder("two")
	if remaining.Status != domain.OrderStatusOpen {
		t.Fatalf("unrelated order = %+v", remaining)
	}
}

func advancedLimit(id domain.OrderID, side domain.Side, price domain.Price, quantity domain.Quantity, tif domain.TimeInForce) domain.PlaceOrder {
	return domain.PlaceOrder{OrderID: id, Side: side, OrderType: domain.OrderTypeLimit, TimeInForce: tif, Price: price, Quantity: quantity}
}
