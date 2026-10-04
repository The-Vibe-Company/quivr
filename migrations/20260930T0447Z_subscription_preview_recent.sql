-- subscription_preview_recent (THE-767)
-- A Subscription preview judges the most recently accepted Record Versions of
-- its Corpora: it walks the Receipts of those Corpora newest first.
CREATE INDEX ingestion_receipts_recent ON ingestion_receipts(organization,corpus_id,accepted_at DESC);
