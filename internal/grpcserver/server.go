package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const DefaultSubscriberBuffer = 64

type idempotencyEntry struct {
	fingerprint string
	response    *ordermatchv1.PlaceOrderResponse
}

// Server adapts the transport contract to the single-writer engine.
type Server struct {
	ordermatchv1.UnimplementedOrderMatchServiceServer
	engine *engine.Engine

	// mutationMu preserves engine completion order through idempotency storage
	// and market-data publication.
	mutationMu  sync.Mutex
	idempotency map[string]idempotencyEntry
	market      *broker
}

func New(e *engine.Engine, subscriberBuffer int) (*Server, error) {
	if e == nil {
		return nil, errors.New("engine is required")
	}
	if subscriberBuffer <= 0 {
		return nil, errors.New("subscriber buffer must be positive")
	}
	return &Server{engine: e, idempotency: make(map[string]idempotencyEntry), market: newBroker(subscriberBuffer)}, nil
}

func (s *Server) PlaceOrder(ctx context.Context, req *ordermatchv1.PlaceOrderRequest) (*ordermatchv1.PlaceOrderResponse, error) {
	if req == nil || req.ClientOrderId == "" {
		return nil, status.Error(codes.InvalidArgument, "client_order_id is required")
	}
	fingerprint := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%d\x00%d", req.OrderId, req.Side, req.OrderType, req.TimeInForce, req.Price, req.Quantity)

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if prior, ok := s.idempotency[req.ClientOrderId]; ok {
		if prior.fingerprint != fingerprint {
			return nil, status.Error(codes.AlreadyExists, "client_order_id was already used with different parameters")
		}
		return proto.Clone(prior.response).(*ordermatchv1.PlaceOrderResponse), nil
	}

	result, err := s.engine.PlaceOrder(ctx, domain.PlaceOrder{
		OrderID: domain.OrderID(req.OrderId), Side: fromProtoSide(req.Side),
		OrderType: fromProtoOrderType(req.OrderType), TimeInForce: fromProtoTimeInForce(req.TimeInForce),
		Price: domain.Price(req.Price), Quantity: domain.Quantity(req.Quantity),
	})
	if err != nil {
		return nil, mapError(err)
	}
	response := &ordermatchv1.PlaceOrderResponse{Order: orderToProto(result.Order), Trades: tradesToProto(result.Trades)}
	response.Sequence = maxSequence(result.Order.Sequence, result.Trades)
	s.idempotency[req.ClientOrderId] = idempotencyEntry{fingerprint: fingerprint, response: proto.Clone(response).(*ordermatchv1.PlaceOrderResponse)}
	s.market.publish(&ordermatchv1.MarketDataUpdate{Sequence: response.Sequence, PreviousSequence: uint64(result.Order.Sequence - 1), Payload: &ordermatchv1.MarketDataUpdate_Delta{Delta: &ordermatchv1.BookDelta{Mutation: ordermatchv1.BookMutation_BOOK_MUTATION_ORDER_ACCEPTED, Order: response.Order, Trades: response.Trades}}})
	return response, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *ordermatchv1.CancelOrderRequest) (*ordermatchv1.CancelOrderResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	order, err := s.engine.CancelOrder(ctx, domain.CancelOrder{OrderID: domain.OrderID(req.OrderId)})
	if err != nil {
		return nil, mapError(err)
	}
	snapshot, err := s.engine.Snapshot(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	response := &ordermatchv1.CancelOrderResponse{Sequence: uint64(snapshot.Sequence), Order: orderToProto(order)}
	s.market.publish(&ordermatchv1.MarketDataUpdate{Sequence: response.Sequence, PreviousSequence: response.Sequence - 1, Payload: &ordermatchv1.MarketDataUpdate_Delta{Delta: &ordermatchv1.BookDelta{Mutation: ordermatchv1.BookMutation_BOOK_MUTATION_ORDER_CANCELED, Order: response.Order}}})
	return response, nil
}

func (s *Server) GetOrder(ctx context.Context, req *ordermatchv1.GetOrderRequest) (*ordermatchv1.GetOrderResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	order, err := s.engine.GetOrder(ctx, domain.OrderID(req.OrderId))
	if err != nil {
		return nil, mapError(err)
	}
	return &ordermatchv1.GetOrderResponse{Sequence: uint64(order.Sequence), Order: orderToProto(order)}, nil
}

func (s *Server) GetBookSnapshot(ctx context.Context, _ *ordermatchv1.GetBookSnapshotRequest) (*ordermatchv1.GetBookSnapshotResponse, error) {
	snapshot, err := s.engine.Snapshot(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	converted := snapshotToProto(snapshot)
	return &ordermatchv1.GetBookSnapshotResponse{Sequence: converted.Sequence, Snapshot: converted}, nil
}

func (s *Server) StreamMarketData(_ *ordermatchv1.StreamMarketDataRequest, stream ordermatchv1.OrderMatchService_StreamMarketDataServer) error {
	// Holding mutationMu makes snapshot creation and subscription registration
	// atomic with respect to published deltas.
	s.mutationMu.Lock()
	snapshot, err := s.engine.Snapshot(stream.Context())
	if err != nil {
		s.mutationMu.Unlock()
		return mapError(err)
	}
	sub := s.market.subscribe()
	s.mutationMu.Unlock()
	defer s.market.unsubscribe(sub.id)

	initial := snapshotToProto(snapshot)
	if err := stream.Send(&ordermatchv1.MarketDataUpdate{Sequence: initial.Sequence, Payload: &ordermatchv1.MarketDataUpdate_Snapshot{Snapshot: initial}}); err != nil {
		return err
	}
	last := initial.Sequence
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case update, ok := <-sub.updates:
			if !ok {
				return status.Errorf(codes.ResourceExhausted, "slow consumer dropped after sequence %d; reconnect for a fresh snapshot", last)
			}
			if update.Sequence <= last {
				continue
			}
			if update.PreviousSequence != last {
				return status.Errorf(codes.DataLoss, "market-data gap: received previous_sequence %d after %d; reconnect for a fresh snapshot", update.PreviousSequence, last)
			}
			if err := stream.Send(update); err != nil {
				return err
			}
			last = update.Sequence
		}
	}
}

func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, domain.ErrInvalidOrderID), errors.Is(err, domain.ErrInvalidTradeID),
		errors.Is(err, domain.ErrInvalidSequence), errors.Is(err, domain.ErrInvalidPrice),
		errors.Is(err, domain.ErrInvalidQuantity), errors.Is(err, domain.ErrInvalidSide),
		errors.Is(err, domain.ErrInvalidOrderType), errors.Is(err, domain.ErrInvalidTimeInForce):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, engine.ErrDuplicateOrderID):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, engine.ErrOrderNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrOrderNotOpen):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, engine.ErrUnsupportedTimeInForce):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, engine.ErrQueueFull):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, engine.ErrEngineClosed):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
