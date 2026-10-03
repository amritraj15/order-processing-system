-- Preserve timestamps: the committed version-4 schema already contains this
-- additive column, and removing it would discard retention metadata. Rolling
-- back this compatibility migration changes only the recorded schema version.
-- 000004 still refuses to drop the table while any durable keys remain.
SELECT 1;
