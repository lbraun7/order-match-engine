package engine

import (
	"testing"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

func BenchmarkOrderBookPlace(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		book := NewOrderBook()
		if _, _, err := book.PlaceOrder(command("order", domain.SideBuy, 100, 1)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOrderBookMatch(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		book := NewOrderBook()
		if _, _, err := book.PlaceOrder(command("maker", domain.SideSell, 100, 1)); err != nil {
			b.Fatal(err)
		}
		_, _, err := book.PlaceOrder(command("taker", domain.SideBuy, 100, 1))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOrderBookCancel(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		book := NewOrderBook()
		if _, _, err := book.PlaceOrder(command("order", domain.SideBuy, 100, 1)); err != nil {
			b.Fatal(err)
		}
		_, err := book.CancelOrder(domain.CancelOrder{OrderID: "order"})
		if err != nil {
			b.Fatal(err)
		}
	}
}
