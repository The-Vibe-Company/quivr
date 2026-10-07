-- Durable keyset progress is internal to rebuild execution. Previous writers
-- omit the constant-default column and can continue scanning without a cursor.
ALTER TABLE operations ADD COLUMN rebuild_cursor text NOT NULL DEFAULT '';
