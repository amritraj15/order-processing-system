-- Removing durable keys would make retries create duplicates after an upgrade.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM order_idempotency) THEN
        RAISE EXCEPTION 'cannot remove nonempty order idempotency records; use a forward repair';
    END IF;
END $$;
DROP TABLE order_idempotency;
