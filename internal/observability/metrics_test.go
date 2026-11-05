package observability

import (
	"context"
	"testing"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"github.com/prometheus/client_golang/prometheus"
)

func TestEngineMetricsExposeQueueAndBookDepth(t *testing.T) {
	router, err := engine.NewRouter(8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })
	if _, err := router.PlaceOrder(context.Background(), "BTC-USD", domain.PlaceOrder{
		OrderID: "bid", Side: domain.SideBuy, OrderType: domain.OrderTypeLimit,
		TimeInForce: domain.TimeInForceGTC, Price: 100, Quantity: 2,
	}); err != nil {
		t.Fatal(err)
	}

	registry := prometheus.NewPedanticRegistry()
	metrics := New(router)
	if err := metrics.Register(registry); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"order_match_book_price_levels":   false,
		"order_match_book_resting_orders": false,
		"order_match_engine_queue_depth":  false,
		"order_match_engine_sequence":     false,
	}
	for _, family := range families {
		if _, ok := wanted[family.GetName()]; ok {
			wanted[family.GetName()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("metric %s was not gathered", name)
		}
	}
}
