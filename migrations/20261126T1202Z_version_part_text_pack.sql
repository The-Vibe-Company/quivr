-- NULL ranges retain the standalone Part object semantics of legacy rows.
ALTER TABLE version_parts
 ADD COLUMN pack_offset BIGINT,
 ADD COLUMN pack_length BIGINT;
