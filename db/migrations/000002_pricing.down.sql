DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM fx_rates) OR EXISTS (
 SELECT 1 FROM orders o CROSS JOIN catalog_settings c WHERE o.currency <> c.base_currency
 ) THEN RAISE EXCEPTION 'pricing rollback requires investigation: rates or differing order currencies exist'; END IF;
END $$;
DROP TABLE fx_rates;
DROP TABLE catalog_settings;
