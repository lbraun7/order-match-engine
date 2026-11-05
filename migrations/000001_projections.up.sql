CREATE TABLE projection_events (
    symbol TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    event_type SMALLINT NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, sequence)
);

CREATE TABLE projection_checkpoint (
    symbol TEXT PRIMARY KEY,
    sequence BIGINT NOT NULL DEFAULT 0 CHECK (sequence >= 0)
);

CREATE TABLE orders (
    symbol TEXT NOT NULL,
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
    updated_event_sequence BIGINT NOT NULL,
    PRIMARY KEY (symbol, order_id),
    UNIQUE (symbol, order_sequence),
    FOREIGN KEY (symbol, updated_event_sequence) REFERENCES projection_events(symbol, sequence),
    CHECK (remaining_quantity + executed_quantity = original_quantity)
);

CREATE TABLE order_history (
    symbol TEXT NOT NULL,
    event_sequence BIGINT NOT NULL,
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
    PRIMARY KEY (symbol, event_sequence, order_id),
    FOREIGN KEY (symbol, event_sequence) REFERENCES projection_events(symbol, sequence) ON DELETE CASCADE,
    CHECK (remaining_quantity + executed_quantity = original_quantity)
);
CREATE INDEX order_history_order_id_event_sequence_idx
    ON order_history (symbol, order_id, event_sequence DESC);

CREATE TABLE trades (
    symbol TEXT NOT NULL,
    trade_id TEXT NOT NULL,
    trade_sequence BIGINT NOT NULL,
    event_sequence BIGINT NOT NULL,
    maker_order_id TEXT NOT NULL,
    taker_order_id TEXT NOT NULL,
    price BIGINT NOT NULL CHECK (price > 0),
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, trade_id),
    UNIQUE (symbol, trade_sequence),
    FOREIGN KEY (symbol, event_sequence) REFERENCES projection_events(symbol, sequence)
);
CREATE INDEX trades_maker_order_id_idx ON trades (symbol, maker_order_id);
CREATE INDEX trades_taker_order_id_idx ON trades (symbol, taker_order_id);
