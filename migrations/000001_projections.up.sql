CREATE TABLE projection_events (
    sequence BIGINT PRIMARY KEY CHECK (sequence > 0),
    event_type SMALLINT NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projection_checkpoint (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    sequence BIGINT NOT NULL DEFAULT 0 CHECK (sequence >= 0)
);
INSERT INTO projection_checkpoint (id, sequence) VALUES (TRUE, 0);

CREATE TABLE orders (
    order_id TEXT PRIMARY KEY,
    order_sequence BIGINT NOT NULL UNIQUE,
    side SMALLINT NOT NULL,
    order_type SMALLINT NOT NULL,
    time_in_force SMALLINT NOT NULL,
    price BIGINT NOT NULL CHECK (price > 0),
    original_quantity BIGINT NOT NULL CHECK (original_quantity > 0),
    remaining_quantity BIGINT NOT NULL CHECK (remaining_quantity >= 0),
    executed_quantity BIGINT NOT NULL CHECK (executed_quantity >= 0),
    status SMALLINT NOT NULL,
    updated_event_sequence BIGINT NOT NULL REFERENCES projection_events(sequence),
    CHECK (remaining_quantity + executed_quantity = original_quantity)
);

CREATE TABLE order_history (
    event_sequence BIGINT NOT NULL REFERENCES projection_events(sequence) ON DELETE CASCADE,
    order_id TEXT NOT NULL,
    order_sequence BIGINT NOT NULL,
    side SMALLINT NOT NULL,
    order_type SMALLINT NOT NULL,
    time_in_force SMALLINT NOT NULL,
    price BIGINT NOT NULL CHECK (price > 0),
    original_quantity BIGINT NOT NULL CHECK (original_quantity > 0),
    remaining_quantity BIGINT NOT NULL CHECK (remaining_quantity >= 0),
    executed_quantity BIGINT NOT NULL CHECK (executed_quantity >= 0),
    status SMALLINT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_sequence, order_id),
    CHECK (remaining_quantity + executed_quantity = original_quantity)
);
CREATE INDEX order_history_order_id_event_sequence_idx
    ON order_history (order_id, event_sequence DESC);

CREATE TABLE trades (
    trade_id TEXT PRIMARY KEY,
    trade_sequence BIGINT NOT NULL UNIQUE,
    event_sequence BIGINT NOT NULL REFERENCES projection_events(sequence),
    maker_order_id TEXT NOT NULL,
    taker_order_id TEXT NOT NULL,
    price BIGINT NOT NULL CHECK (price > 0),
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX trades_maker_order_id_idx ON trades (maker_order_id);
CREATE INDEX trades_taker_order_id_idx ON trades (taker_order_id);
