-- Receipt identity is (organization, route_family, idempotency_key). Withdrawal
-- uses its own route family so a client can safely reuse a key across the
-- ingestion and withdrawal families without a false conflict.
ALTER TABLE ingestion_receipts ADD COLUMN route_family text NOT NULL DEFAULT 'ingestion';
ALTER TABLE ingestion_receipts DROP CONSTRAINT ingestion_receipts_organization_request_key_key;
ALTER TABLE ingestion_receipts ADD CONSTRAINT ingestion_receipts_org_route_key UNIQUE(organization, route_family, request_key);
ALTER TABLE ingestion_receipts ADD CONSTRAINT ingestion_receipts_route_family_check CHECK (route_family IN ('ingestion','withdrawal'));
