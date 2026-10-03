-- Earlier installations of version 4 may not have created_at. Keep the
-- committed 000004 unchanged and repair either version-4 schema safely.
-- Existing keys without a timestamp receive the upgrade time, conservatively
-- starting their retention age then rather than guessing their original age.
ALTER TABLE order_idempotency
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();
