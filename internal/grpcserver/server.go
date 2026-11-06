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

type symbolState struct {
	// mutationMu preserves this symbol's engine completion order through
	// idempotency storage and market-data publication without serializing
	// unrelated symbols.
	mutationMu  sync.Mutex
	idempotency map[string]idempotencyEntry
	market      *broker
}

// Server adapts the transport contract to the single-writer engine.
type Server struct {
	ordermatchv1.UnimplementedOrderMatchServiceServer
	router           *engine.Router
	subscriberBuffer int
	statesMu         sync.Mutex
	states           map[domain.Symbol]*symbolState
}

func New(router *engine.Router, subscriberBuffer int) (*Server, error) {
	if router == nil {
		return nil, errors.New("engine router is required")
	}
	if subscriberBuffer <= 0 {
		return nil, errors.New("subscriber buffer must be positive")
	}
	return &Server{router: router, subscriberBuffer: subscriberBuffer, states: make(map[domain.Symbol]*symbolState)}, nil
}

func (s *Server) state(symbol domain.Symbol) *symbolState {
	s.statesMu.Lock()
	defer s.statesMu.Unlock()
	state := s.states[symbol]
	if state == nil {
		state = &symbolState{idempotency: make(map[string]idempotencyEntry), market: newBroker(s.subscriberBuffer)}
		s.states[symbol] = state
	}
	return state
}

func (s *Server) PlaceOrder(ctx context.Context, req *ordermatchv1.PlaceOrderRequest) (*ordermatchv1.PlaceOrderResponse, error) {
	if req == nil || req.ClientOrderId == "" {
		return nil, status.Error(codes.InvalidArgument, "client_order_id is required")
	}
	symbol := domain.Symbol(req.Symbol)
	if err := symbol.Validate(); err != nil {
		return nil, mapError(err)
	}
	fingerprint := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%d\x00%d\x00%d",
		req.OrderId, req.Side, req.OrderType, req.TimeInForce, req.Price, req.Quantity,
		req.ParticipantId, req.StopPrice, req.DisplayQuantity, req.SelfTradePrevention)

	state := s.state(symbol)
	state.mutationMu.Lock()
	defer state.mutationMu.Unlock()
	if prior, ok := state.idempotency[req.ClientOrderId]; ok {
		if prior.fingerprint != fingerprint {
			return nil, status.Error(codes.AlreadyExists, "client_order_id was already used with different parameters")
		}
		return proto.Clone(prior.response).(*ordermatchv1.PlaceOrderResponse), nil
	}

	result, err := s.router.PlaceOrder(ctx, symbol, domain.PlaceOrder{
		OrderID: domain.OrderID(req.OrderId), Side: fromProtoSide(req.Side),
		OrderType: fromProtoOrderType(req.OrderType), TimeInForce: fromProtoTimeInForce(req.TimeInForce),
		Price: domain.Price(req.Price), Quantity: domain.Quantity(req.Quantity), ParticipantID: domain.ParticipantID(req.ParticipantId),
		StopPrice: domain.Price(req.StopPrice), DisplayQuantity: domain.Quantity(req.DisplayQuantity),
		SelfTradePrevention: domain.SelfTradePrevention(req.SelfTradePrevention),
	})
	if err != nil {
		return nil, mapError(err)
	}
	response := &ordermatchv1.PlaceOrderResponse{Order: orderToProto(result.Order), Trades: tradesToProto(result.Trades), AffectedOrders: ordersToProto(result.AffectedOrders)}
	response.Sequence = maxSequence(result.Order.Sequence, result.Trades)
	state.idempotency[req.ClientOrderId] = idempotencyEntry{fingerprint: fingerprint, response: proto.Clone(response).(*ordermatchv1.PlaceOrderResponse)}
	state.market.publish(&ordermatchv1.MarketDataUpdate{Symbol: req.Symbol, Sequence: response.Sequence, PreviousSequence: uint64(result.Order.Sequence - 1), Payload: &ordermatchv1.MarketDataUpdate_Delta{Delta: &ordermatchv1.BookDelta{Mutation: ordermatchv1.BookMutation_BOOK_MUTATION_ORDER_ACCEPTED, Order: response.Order, Trades: response.Trades, AffectedOrders: ordersToProto(result.AffectedOrders)}}})
	return response, nil
}

func (s *Server) MassCancel(ctx context.Context, req *ordermatchv1.MassCancelRequest) (*ordermatchv1.MassCancelResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if req.Symbol == "" {
		participant := domain.ParticipantID(req.ParticipantId)
		if err := participant.Validate(); err != nil {
			return nil, mapError(err)
		}
		response := &ordermatchv1.MassCancelResponse{}
		for _, symbol := range s.router.Symbols() {
			result, err := s.massCancelSymbol(ctx, symbol, participant)
			if err != nil {
				return nil, err
			}
			response.Results = append(response.Results, result)
		}
		return response, nil
	}
	symbol := domain.Symbol(req.Symbol)
	if err := symbol.Validate(); err != nil {
		return nil, mapError(err)
	}
	result, err := s.massCancelSymbol(ctx, symbol, domain.ParticipantID(req.ParticipantId))
	if err != nil {
		return nil, err
	}
	return &ordermatchv1.MassCancelResponse{Sequence: result.Sequence, Orders: result.Orders, Results: []*ordermatchv1.SymbolMassCancelResult{result}}, nil
}

func (s *Server) massCancelSymbol(ctx context.Context, symbol domain.Symbol, participant domain.ParticipantID) (*ordermatchv1.SymbolMassCancelResult, error) {
	state := s.state(symbol)
	state.mutationMu.Lock()
	defer state.mutationMu.Unlock()
	result, err := s.router.MassCancel(ctx, symbol, domain.MassCancel{ParticipantID: participant})
	if err != nil {
		return nil, mapError(err)
	}
	response := &ordermatchv1.SymbolMassCancelResult{Symbol: string(symbol), Sequence: uint64(result.Sequence), Orders: ordersToProto(result.Orders)}
	state.market.publish(&ordermatchv1.MarketDataUpdate{Symbol: string(symbol), Sequence: response.Sequence, PreviousSequence: response.Sequence - 1, Payload: &ordermatchv1.MarketDataUpdate_Delta{Delta: &ordermatchv1.BookDelta{Mutation: ordermatchv1.BookMutation_BOOK_MUTATION_MASS_CANCELED, AffectedOrders: response.Orders}}})
	return response, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *ordermatchv1.CancelOrderRequest) (*ordermatchv1.CancelOrderResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	symbol := domain.Symbol(req.Symbol)
	if err := symbol.Validate(); err != nil {
		return nil, mapError(err)
	}
	state := s.state(symbol)
	state.mutationMu.Lock()
	defer state.mutationMu.Unlock()
	order, err := s.router.CancelOrder(ctx, symbol, domain.CancelOrder{OrderID: domain.OrderID(req.OrderId)})
	if err != nil {
		return nil, mapError(err)
	}
	snapshot, err := s.router.Snapshot(ctx, symbol)
	if err != nil {
		return nil, mapError(err)
	}
	response := &ordermatchv1.CancelOrderResponse{Sequence: uint64(snapshot.Sequence), Order: orderToProto(order)}
	state.market.publish(&ordermatchv1.MarketDataUpdate{Symbol: req.Symbol, Sequence: response.Sequence, PreviousSequence: response.Sequence - 1, Payload: &ordermatchv1.MarketDataUpdate_Delta{Delta: &ordermatchv1.BookDelta{Mutation: ordermatchv1.BookMutation_BOOK_MUTATION_ORDER_CANCELED, Order: response.Order}}})
	return response, nil
}

func (s *Server) GetOrder(ctx context.Context, req *ordermatchv1.GetOrderRequest) (*ordermatchv1.GetOrderResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	symbol := domain.Symbol(req.Symbol)
	order, err := s.router.GetOrder(ctx, symbol, domain.OrderID(req.OrderId))
	if err != nil {
		return nil, mapError(err)
	}
	return &ordermatchv1.GetOrderResponse{Sequence: uint64(order.Sequence), Order: orderToProto(order)}, nil
}

func (s *Server) GetBookSnapshot(ctx context.Context, req *ordermatchv1.GetBookSnapshotRequest) (*ordermatchv1.GetBookSnapshotResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	symbol := domain.Symbol(req.Symbol)
	snapshot, err := s.router.Snapshot(ctx, symbol)
	if err != nil {
		return nil, mapError(err)
	}
	converted := snapshotToProto(snapshot)
	converted.Symbol = req.Symbol
	return &ordermatchv1.GetBookSnapshotResponse{Sequence: converted.Sequence, Snapshot: converted}, nil
}

func (s *Server) StreamMarketData(req *ordermatchv1.StreamMarketDataRequest, stream ordermatchv1.OrderMatchService_StreamMarketDataServer) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "request is required")
	}
	symbol := domain.Symbol(req.Symbol)
	if err := symbol.Validate(); err != nil {
		return mapError(err)
	}
	state := s.state(symbol)
	// Holding mutationMu makes snapshot creation and subscription registration
	// atomic with respect to published deltas.
	state.mutationMu.Lock()
	snapshot, err := s.router.Snapshot(stream.Context(), symbol)
	if err != nil {
		state.mutationMu.Unlock()
		return mapError(err)
	}
	sub := state.market.subscribe()
	state.mutationMu.Unlock()
	defer state.market.unsubscribe(sub.id)

	initial := snapshotToProto(snapshot)
	initial.Symbol = req.Symbol
	if err := stream.Send(&ordermatchv1.MarketDataUpdate{Symbol: req.Symbol, Sequence: initial.Sequence, Payload: &ordermatchv1.MarketDataUpdate_Snapshot{Snapshot: initial}}); err != nil {
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
		errors.Is(err, domain.ErrInvalidSymbol), errors.Is(err, domain.ErrInvalidParticipantID),
		errors.Is(err, domain.ErrInvalidSequence), errors.Is(err, domain.ErrInvalidPrice),
		errors.Is(err, domain.ErrInvalidQuantity), errors.Is(err, domain.ErrInvalidSide),
		errors.Is(err, domain.ErrInvalidOrderType), errors.Is(err, domain.ErrInvalidTimeInForce),
		errors.Is(err, domain.ErrInvalidSelfTradePrevention), errors.Is(err, domain.ErrInvalidDisplayQuantity),
		errors.Is(err, domain.ErrInvalidStopPrice), errors.Is(err, engine.ErrInvalidOrderSemantics):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, engine.ErrDuplicateOrderID):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, engine.ErrOrderNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrOrderNotOpen):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, engine.ErrQueueFull):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, engine.ErrEngineClosed), errors.Is(err, engine.ErrRouterClosed):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
