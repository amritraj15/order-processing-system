DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM order_quotes) OR EXISTS (SELECT 1 FROM orders WHERE pricing_mode <> 'legacy')
 THEN RAISE EXCEPTION 'quote rollback would lose pricing provenance; use forward repair'; END IF;
END $$;
DROP TABLE quote_items;
DROP TABLE order_quotes;
ALTER TABLE order_items DROP CONSTRAINT source_item_totals_check,
 DROP COLUMN source_unit_price_minor, DROP COLUMN source_line_total_minor;
ALTER TABLE orders DROP CONSTRAINT orders_pricing_check,
 DROP COLUMN quote_id, DROP COLUMN pricing_mode, DROP COLUMN region, DROP COLUMN mapping_version,
 DROP COLUMN source_currency, DROP COLUMN base_digits, DROP COLUMN target_digits,
 DROP COLUMN rate, DROP COLUMN rate_id, DROP COLUMN rate_source,
 DROP COLUMN rate_valid_from, DROP COLUMN rate_valid_until, DROP COLUMN source_total_minor;
