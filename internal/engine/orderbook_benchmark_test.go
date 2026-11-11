package engine

import (
	"container/heap"
	"fmt"
	"testing"

	"github.com/lbraun7/order-match-engine/internal/domain"
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
				prices = insertPrice(prices, domain.Price(levels+1), false)
				if len(prices) != levels+1 {
					b.Fatal("price was not inserted")
				}
			}
		})
	}
}

// BenchmarkPriceIndexAlternatives provides the evidence gate for replacing
// the sorted slice. It includes the same per-iteration copy in both variants
// so mutations remain independent.
func BenchmarkPriceIndexAlternatives(b *testing.B) {
	for _, levels := range []int{100, 1_000, 10_000} {
		seed := make([]domain.Price, levels)
		for index := range seed {
			seed[index] = domain.Price(index * 2)
		}
		price := domain.Price(levels + 1)
		b.Run(fmt.Sprintf("sorted-slice/levels=%d", levels), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				prices := append([]domain.Price(nil), seed...)
				prices = insertPrice(prices, price, false)
			}
		})
		b.Run(fmt.Sprintf("binary-heap/levels=%d", levels), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				prices := priceHeap(append([]domain.Price(nil), seed...))
				heap.Push(&prices, price)
			}
		})
	}
}

type priceHeap []domain.Price

func (p priceHeap) Len() int           { return len(p) }
func (p priceHeap) Less(i, j int) bool { return p[i] < p[j] }
func (p priceHeap) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p *priceHeap) Push(value any)    { *p = append(*p, value.(domain.Price)) }
func (p *priceHeap) Pop() any {
	old := *p
	value := old[len(old)-1]
	*p = old[:len(old)-1]
	return value
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
