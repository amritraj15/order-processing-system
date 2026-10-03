CREATE TABLE catalog_settings (
 singleton_id SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
 base_currency CHAR(3) NOT NULL CHECK (base_currency IN ('USD','INR','GBP','JPY','KWD','EUR')),
 initialized_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE fx_rates (
 id UUID PRIMARY KEY,
 base_currency CHAR(3) NOT NULL,
 target_currency CHAR(3) NOT NULL,
 rate NUMERIC(24,12) NOT NULL CHECK (rate > 0),
 valid_from TIMESTAMPTZ NOT NULL,
 valid_until TIMESTAMPTZ NOT NULL CHECK (valid_until > valid_from),
 source VARCHAR(128) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 CHECK (base_currency <> target_currency)
);
CREATE INDEX fx_rates_pair_validity_idx ON fx_rates (base_currency,target_currency,valid_from,valid_until);
