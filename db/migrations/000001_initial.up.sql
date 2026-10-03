CREATE TABLE users (
    id UUID PRIMARY KEY,
    name VARCHAR(64) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role VARCHAR(16) NOT NULL CHECK (role IN ('customer', 'admin')),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE denylisted_tokens (
    token_hash CHAR(64) PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX denylisted_tokens_expiry_idx ON denylisted_tokens (expires_at);
CREATE TABLE products (
    id UUID PRIMARY KEY,
    sku VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    price_minor BIGINT NOT NULL CHECK (price_minor > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE orders (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES users(id),
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'PROCESSING', 'SHIPPED', 'DELIVERED', 'CANCELLED')),
    currency CHAR(3) NOT NULL,
    total_minor BIGINT NOT NULL CHECK (total_minor > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE order_items (
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    product_id UUID NOT NULL REFERENCES products(id),
    sku VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL,
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    unit_price_minor BIGINT NOT NULL CHECK (unit_price_minor > 0),
    line_total_minor BIGINT NOT NULL CHECK (line_total_minor > 0),
    PRIMARY KEY (order_id, position),
    UNIQUE (order_id, product_id),
    CHECK (line_total_minor = quantity * unit_price_minor)
);
-- Historical orders never enter the worker's queue index.
CREATE INDEX orders_pending_idx ON orders (created_at, id) WHERE status = 'PENDING';
CREATE INDEX orders_customer_id_idx ON orders (customer_id, id DESC);
CREATE INDEX orders_customer_status_id_idx ON orders (customer_id, status, id DESC);
CREATE INDEX orders_status_id_idx ON orders (status, id DESC);
