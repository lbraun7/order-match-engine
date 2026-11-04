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

func (s *PostgresStore) Checkpoint(ctx context.Context) (uint64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM projection_checkpoint WHERE id = TRUE`).Scan(&sequence)
	if err != nil {
		return 0, err
	}
	return uint64(sequence), nil
}

func (s *PostgresStore) Apply(ctx context.Context, event Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO projection_events (sequence, event_type)
		VALUES ($1, $2)
		ON CONFLICT (sequence) DO NOTHING`, event.EventSequence, event.EventType)
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
				order_id, order_sequence, side, order_type, time_in_force, price,
				original_quantity, remaining_quantity, executed_quantity, status, updated_event_sequence
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (order_id) DO UPDATE SET
				remaining_quantity = EXCLUDED.remaining_quantity,
				executed_quantity = EXCLUDED.executed_quantity,
				status = EXCLUDED.status,
				updated_event_sequence = EXCLUDED.updated_event_sequence`,
			order.ID, order.Sequence, order.Side, order.Type, order.TimeInForce, order.Price,
			order.OriginalQuantity, order.RemainingQuantity, order.ExecutedQuantity, order.Status, event.EventSequence)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO order_history (
				event_sequence, order_id, order_sequence, side, order_type, time_in_force,
				price, original_quantity, remaining_quantity, executed_quantity, status
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (event_sequence, order_id) DO NOTHING`,
			event.EventSequence, order.ID, order.Sequence, order.Side, order.Type, order.TimeInForce,
			order.Price, order.OriginalQuantity, order.RemainingQuantity, order.ExecutedQuantity, order.Status)
		if err != nil {
			return err
		}
	}
	for _, trade := range event.Trades {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO trades (
				trade_id, trade_sequence, event_sequence, maker_order_id, taker_order_id, price, quantity
			) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (trade_id) DO NOTHING`,
			trade.ID(), trade.Sequence(), event.EventSequence, trade.MakerOrderID(), trade.TakerOrderID(), trade.Price(), trade.Quantity())
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE projection_checkpoint SET sequence = GREATEST(sequence, $1) WHERE id = TRUE`, event.EventSequence)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `TRUNCATE trades, order_history, orders, projection_events`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projection_checkpoint SET sequence = 0 WHERE id = TRUE`); err != nil {
		return err
	}
	return tx.Commit()
}
