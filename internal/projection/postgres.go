package projection

import (
	"context"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore is the query-model writer. Apply uses one transaction for the
// event marker, rows, and checkpoint, so a retry can never expose a partial
// projection update.
type PostgresStore struct {
	db *sql.DB
}

type postgresSymbolStore struct {
	db     *sql.DB
	symbol string
}

func OpenPostgres(databaseURL string) (*PostgresStore, error) {
	if databaseURL == "" {
		return nil, ErrInvalidConfig
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ForSymbol scopes projection idempotency, checkpoints, orders, and trades to
// one independently sequenced book.
func (s *PostgresStore) ForSymbol(symbol string) Store {
	return &postgresSymbolStore{db: s.db, symbol: symbol}
}

func (s *postgresSymbolStore) Checkpoint(ctx context.Context) (uint64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO projection_checkpoint (symbol, sequence) VALUES ($1, 0)
		ON CONFLICT (symbol) DO UPDATE SET symbol = EXCLUDED.symbol
		RETURNING sequence`, s.symbol).Scan(&sequence)
	if err != nil {
		return 0, err
	}
	return uint64(sequence), nil
}

func (s *postgresSymbolStore) Apply(ctx context.Context, event Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO projection_events (symbol, sequence, event_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (symbol, sequence) DO NOTHING`, s.symbol, event.EventSequence, event.EventType)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit()
	}

	for _, order := range event.Orders {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO orders (
				symbol, order_id, order_sequence, participant_id, side, order_type, time_in_force, price,
				original_quantity, remaining_quantity, executed_quantity, status, stop_price, display_quantity,
				visible_quantity, self_trade_prevention, dormant, updated_event_sequence
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (symbol, order_id) DO UPDATE SET
				remaining_quantity = EXCLUDED.remaining_quantity,
				executed_quantity = EXCLUDED.executed_quantity,
				status = EXCLUDED.status,
				visible_quantity = EXCLUDED.visible_quantity,
				dormant = EXCLUDED.dormant,
				updated_event_sequence = EXCLUDED.updated_event_sequence`,
			s.symbol, order.ID, order.Sequence, order.ParticipantID, order.Side, order.Type, order.TimeInForce, order.Price,
			order.OriginalQuantity, order.RemainingQuantity, order.ExecutedQuantity, order.Status, order.StopPrice,
			order.DisplayQuantity, order.VisibleQuantity, order.SelfTradePrevention, order.Dormant, event.EventSequence)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO order_history (
				symbol, event_sequence, order_id, order_sequence, participant_id, side, order_type, time_in_force,
				price, original_quantity, remaining_quantity, executed_quantity, status, stop_price, display_quantity,
				visible_quantity, self_trade_prevention, dormant
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (symbol, event_sequence, order_id) DO NOTHING`,
			s.symbol, event.EventSequence, order.ID, order.Sequence, order.ParticipantID, order.Side, order.Type, order.TimeInForce,
			order.Price, order.OriginalQuantity, order.RemainingQuantity, order.ExecutedQuantity, order.Status,
			order.StopPrice, order.DisplayQuantity, order.VisibleQuantity, order.SelfTradePrevention, order.Dormant)
		if err != nil {
			return err
		}
	}
	for _, trade := range event.Trades {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO trades (
				symbol, trade_id, trade_sequence, event_sequence, maker_order_id, taker_order_id, price, quantity
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (symbol, trade_id) DO NOTHING`,
			s.symbol, trade.ID(), trade.Sequence(), event.EventSequence, trade.MakerOrderID(), trade.TakerOrderID(), trade.Price(), trade.Quantity())
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE projection_checkpoint SET sequence = GREATEST(sequence, $1) WHERE symbol = $2`, event.EventSequence, s.symbol)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresSymbolStore) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM trades WHERE symbol = $1`, s.symbol); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM order_history WHERE symbol = $1`, s.symbol); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM orders WHERE symbol = $1`, s.symbol); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projection_events WHERE symbol = $1`, s.symbol); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projection_checkpoint (symbol, sequence) VALUES ($1, 0) ON CONFLICT (symbol) DO UPDATE SET sequence = 0`, s.symbol); err != nil {
		return err
	}
	return tx.Commit()
}
