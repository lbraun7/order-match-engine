ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_price_check;
ALTER TABLE orders ADD CONSTRAINT orders_price_check CHECK (price >= 0);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS participant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS stop_price BIGINT NOT NULL DEFAULT 0 CHECK (stop_price >= 0);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS display_quantity BIGINT NOT NULL DEFAULT 0 CHECK (display_quantity >= 0);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS visible_quantity BIGINT NOT NULL DEFAULT 0 CHECK (visible_quantity >= 0);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS self_trade_prevention SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS dormant BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE order_history DROP CONSTRAINT IF EXISTS order_history_price_check;
ALTER TABLE order_history ADD CONSTRAINT order_history_price_check CHECK (price >= 0);
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS participant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS stop_price BIGINT NOT NULL DEFAULT 0 CHECK (stop_price >= 0);
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS display_quantity BIGINT NOT NULL DEFAULT 0 CHECK (display_quantity >= 0);
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS visible_quantity BIGINT NOT NULL DEFAULT 0 CHECK (visible_quantity >= 0);
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS self_trade_prevention SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE order_history ADD COLUMN IF NOT EXISTS dormant BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS orders_participant_id_idx ON orders (symbol, participant_id);
