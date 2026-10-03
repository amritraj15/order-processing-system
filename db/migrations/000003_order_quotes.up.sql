ALTER TABLE orders ADD COLUMN quote_id UUID UNIQUE,
 ADD COLUMN pricing_mode VARCHAR(8),
 ADD COLUMN region CHAR(2), ADD COLUMN mapping_version VARCHAR(32),
 ADD COLUMN source_currency CHAR(3), ADD COLUMN base_digits SMALLINT,
 ADD COLUMN target_digits SMALLINT, ADD COLUMN rate NUMERIC(24,12),
 ADD COLUMN rate_id UUID REFERENCES fx_rates(id), ADD COLUMN rate_source VARCHAR(128),
 ADD COLUMN rate_valid_from TIMESTAMPTZ, ADD COLUMN rate_valid_until TIMESTAMPTZ,
 ADD COLUMN source_total_minor BIGINT;
UPDATE orders SET pricing_mode = 'legacy';
ALTER TABLE orders ALTER COLUMN pricing_mode SET NOT NULL;
ALTER TABLE orders ADD CONSTRAINT orders_pricing_check CHECK (
 pricing_mode = 'legacy' OR (
 pricing_mode IN ('base','quote') AND source_currency IS NOT NULL AND
 base_digits IS NOT NULL AND base_digits BETWEEN 0 AND 3 AND
 target_digits IS NOT NULL AND target_digits BETWEEN 0 AND 3 AND
 rate IS NOT NULL AND rate > 0 AND rate_source IS NOT NULL AND
 source_total_minor IS NOT NULL AND source_total_minor > 0 AND
 (pricing_mode <> 'base' OR (rate = 1 AND source_currency = currency AND quote_id IS NULL)) AND
 (pricing_mode <> 'quote' OR (quote_id IS NOT NULL AND region IS NOT NULL AND mapping_version IS NOT NULL)) AND
 (source_currency = currency OR (rate_id IS NOT NULL AND rate_valid_from IS NOT NULL AND rate_valid_until IS NOT NULL AND rate_valid_until > rate_valid_from))
 ));
ALTER TABLE order_items ADD COLUMN source_unit_price_minor BIGINT,
 ADD COLUMN source_line_total_minor BIGINT,
 ADD CONSTRAINT source_item_totals_check CHECK (
 (source_unit_price_minor IS NULL AND source_line_total_minor IS NULL) OR
 (source_unit_price_minor IS NOT NULL AND source_unit_price_minor > 0 AND
 source_line_total_minor IS NOT NULL AND source_line_total_minor > 0 AND
 source_line_total_minor = source_unit_price_minor * quantity));
CREATE TABLE order_quotes (
 id UUID PRIMARY KEY, customer_id UUID NOT NULL REFERENCES users(id),
 region CHAR(2) NOT NULL, mapping_version VARCHAR(32) NOT NULL,
 source_currency CHAR(3) NOT NULL, currency CHAR(3) NOT NULL,
 base_digits SMALLINT NOT NULL CHECK (base_digits BETWEEN 0 AND 3),
 target_digits SMALLINT NOT NULL CHECK (target_digits BETWEEN 0 AND 3),
 rate NUMERIC(24,12) NOT NULL CHECK (rate > 0), rate_id UUID REFERENCES fx_rates(id),
 rate_source VARCHAR(128) NOT NULL, rate_valid_from TIMESTAMPTZ, rate_valid_until TIMESTAMPTZ,
 source_total_minor BIGINT NOT NULL CHECK (source_total_minor > 0),
 total_minor BIGINT NOT NULL CHECK (total_minor > 0),
 created_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
 consumed_order_id UUID UNIQUE REFERENCES orders(id),
 CHECK ((source_currency = currency AND rate = 1) OR
 (rate_id IS NOT NULL AND rate_valid_from IS NOT NULL AND rate_valid_until IS NOT NULL AND rate_valid_until > rate_valid_from))
);
CREATE INDEX order_quotes_expiry_idx ON order_quotes (expires_at);
CREATE TABLE quote_items (
 quote_id UUID NOT NULL REFERENCES order_quotes(id) ON DELETE CASCADE,
 position INTEGER NOT NULL CHECK (position >= 0), product_id UUID NOT NULL REFERENCES products(id),
 sku VARCHAR(64) NOT NULL, name VARCHAR(255) NOT NULL,
 quantity BIGINT NOT NULL CHECK (quantity > 0),
 source_unit_price_minor BIGINT NOT NULL CHECK (source_unit_price_minor > 0),
 source_line_total_minor BIGINT NOT NULL CHECK (source_line_total_minor > 0),
 unit_price_minor BIGINT NOT NULL CHECK (unit_price_minor > 0),
 line_total_minor BIGINT NOT NULL CHECK (line_total_minor > 0),
 PRIMARY KEY (quote_id,position), UNIQUE (quote_id,product_id),
 CHECK (source_line_total_minor = source_unit_price_minor * quantity),
 CHECK (line_total_minor = unit_price_minor * quantity)
);
