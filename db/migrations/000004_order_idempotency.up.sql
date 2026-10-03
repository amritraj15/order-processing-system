CREATE TABLE order_idempotency (
    customer_id UUID NOT NULL REFERENCES users(id),
    idempotency_key VARCHAR(128) COLLATE "C" NOT NULL
        CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
    request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    order_id UUID NOT NULL REFERENCES orders(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (customer_id, idempotency_key)
);
