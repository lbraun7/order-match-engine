package grpcserver

import (
	"sync"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
)

type subscription struct {
	id      uint64
	updates chan *ordermatchv1.MarketDataUpdate
}

// broker deliberately drops a slow subscriber as a unit instead of dropping
// individual deltas. Reconnecting clients receive a fresh snapshot.
type broker struct {
	mu          sync.Mutex
	nextID      uint64
	bufferSize  int
	subscribers map[uint64]chan *ordermatchv1.MarketDataUpdate
}

func newBroker(bufferSize int) *broker {
	return &broker{bufferSize: bufferSize, subscribers: make(map[uint64]chan *ordermatchv1.MarketDataUpdate)}
}

func (b *broker) subscribe() subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	s := subscription{id: b.nextID, updates: make(chan *ordermatchv1.MarketDataUpdate, b.bufferSize)}
	b.subscribers[s.id] = s.updates
	return s
}

func (b *broker) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subscribers[id]; ok {
		delete(b.subscribers, id)
		close(ch)
	}
}

func (b *broker) publish(update *ordermatchv1.MarketDataUpdate) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subscribers {
		select {
		case ch <- update:
		default:
			delete(b.subscribers, id)
			close(ch)
		}
	}
}
