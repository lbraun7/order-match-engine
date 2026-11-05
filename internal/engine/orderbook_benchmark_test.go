package engine

import (
	"fmt"
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

// BenchmarkOrderBookAddPriceLevel isolates insertion into a populated price
// index. It is the benchmark used by the profiling workflow because it avoids
// measuring order-book construction in every timed iteration.
func BenchmarkOrderBookAddPriceLevel(b *testing.B) {
	for _, levels := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("levels=%d", levels), func(b *testing.B) {
			seed := make([]domain.Price, levels)
			for i := range seed {
				seed[i] = domain.Price(i * 2)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				prices := append([]domain.Price(nil), seed...)
				prices = insertPrice(prices, domain.Price(levels), false)
				if len(prices) != levels+1 {
					b.Fatal("price was not inserted")
				}
			}
		})
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
