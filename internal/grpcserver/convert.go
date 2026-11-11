package grpcserver

import (
	ordermatchv1 "github.com/lbraun7/order-match-engine/api/v1"
	"github.com/lbraun7/order-match-engine/internal/domain"
	"github.com/lbraun7/order-match-engine/internal/engine"
)

func fromProtoSide(value ordermatchv1.Side) domain.Side { return domain.Side(value) }
func fromProtoOrderType(value ordermatchv1.OrderType) domain.OrderType {
	return domain.OrderType(value)
}
func fromProtoTimeInForce(value ordermatchv1.TimeInForce) domain.TimeInForce {
	return domain.TimeInForce(value)
}

func orderToProto(order engine.OrderSnapshot) *ordermatchv1.Order {
	return &ordermatchv1.Order{
		OrderId: string(order.ID), Sequence: uint64(order.Sequence), Side: ordermatchv1.Side(order.Side),
		OrderType: ordermatchv1.OrderType(order.Type), TimeInForce: ordermatchv1.TimeInForce(order.TimeInForce),
		Price: int64(order.Price), OriginalQuantity: int64(order.OriginalQuantity),
		RemainingQuantity: int64(order.RemainingQuantity), ExecutedQuantity: int64(order.ExecutedQuantity),
		Status: ordermatchv1.OrderStatus(order.Status), ParticipantId: string(order.ParticipantID),
		StopPrice: int64(order.StopPrice), DisplayQuantity: int64(order.DisplayQuantity),
		VisibleQuantity: int64(order.VisibleQuantity), SelfTradePrevention: ordermatchv1.SelfTradePrevention(order.SelfTradePrevention),
		Dormant: order.Dormant,
	}
}

func ordersToProto(orders []engine.OrderSnapshot) []*ordermatchv1.Order {
	result := make([]*ordermatchv1.Order, 0, len(orders))
	for _, order := range orders {
		result = append(result, orderToProto(order))
	}
	return result
}

func tradeToProto(trade domain.Trade) *ordermatchv1.Trade {
	return &ordermatchv1.Trade{TradeId: string(trade.ID()), Sequence: uint64(trade.Sequence()), MakerOrderId: string(trade.MakerOrderID()), TakerOrderId: string(trade.TakerOrderID()), Price: int64(trade.Price()), Quantity: int64(trade.Quantity())}
}

func tradesToProto(trades []domain.Trade) []*ordermatchv1.Trade {
	result := make([]*ordermatchv1.Trade, 0, len(trades))
	for _, trade := range trades {
		result = append(result, tradeToProto(trade))
	}
	return result
}

func snapshotToProto(snapshot engine.BookSnapshot) *ordermatchv1.BookSnapshot {
	return &ordermatchv1.BookSnapshot{Sequence: uint64(snapshot.Sequence), Bids: levelsToProto(snapshot.Bids), Asks: levelsToProto(snapshot.Asks)}
}

func levelsToProto(levels []engine.PriceLevelSnapshot) []*ordermatchv1.PriceLevel {
	result := make([]*ordermatchv1.PriceLevel, 0, len(levels))
	for _, level := range levels {
		converted := &ordermatchv1.PriceLevel{Price: int64(level.Price), TotalQuantity: int64(level.TotalQuantity)}
		for _, order := range level.Orders {
			converted.Orders = append(converted.Orders, orderToProto(order))
		}
		result = append(result, converted)
	}
	return result
}

func maxSequence(orderSequence domain.Sequence, trades []domain.Trade) uint64 {
	sequence := uint64(orderSequence)
	for _, trade := range trades {
		if uint64(trade.Sequence()) > sequence {
			sequence = uint64(trade.Sequence())
		}
	}
	return sequence
}
