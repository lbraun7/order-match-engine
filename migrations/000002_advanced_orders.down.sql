DROP INDEX IF EXISTS orders_participant_id_idx;

ALTER TABLE order_history DROP COLUMN IF EXISTS dormant;
ALTER TABLE order_history DROP COLUMN IF EXISTS self_trade_prevention;
ALTER TABLE order_history DROP COLUMN IF EXISTS visible_quantity;
ALTER TABLE order_history DROP COLUMN IF EXISTS display_quantity;
ALTER TABLE order_history DROP COLUMN IF EXISTS stop_price;
ALTER TABLE order_history DROP COLUMN IF EXISTS participant_id;
ALTER TABLE order_history DROP CONSTRAINT IF EXISTS order_history_price_check;
ALTER TABLE order_history ADD CONSTRAINT order_history_price_check CHECK (price > 0);

ALTER TABLE orders DROP COLUMN IF EXISTS dormant;
ALTER TABLE orders DROP COLUMN IF EXISTS self_trade_prevention;
ALTER TABLE orders DROP COLUMN IF EXISTS visible_quantity;
ALTER TABLE orders DROP COLUMN IF EXISTS display_quantity;
ALTER TABLE orders DROP COLUMN IF EXISTS stop_price;
ALTER TABLE orders DROP COLUMN IF EXISTS participant_id;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_price_check;
ALTER TABLE orders ADD CONSTRAINT orders_price_check CHECK (price > 0);
