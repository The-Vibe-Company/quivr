-- The Record catalog is traversed per Corpus in stable byte-wise key order.
CREATE INDEX records_catalog_order ON records (organization, corpus_id, id COLLATE "C");
