-- Keep the current Version's acceptance date beside its Record so a catalog
-- page/count can use one index without traversing historical Versions.
ALTER TABLE records ADD COLUMN current_accepted_at timestamptz;

UPDATE records r SET current_accepted_at=rc.accepted_at
FROM record_versions v
JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
WHERE (r.organization,r.current_version_id)=(v.organization,v.id);

-- Both currentness publication paths update this pointer. Maintain its date in
-- the same transaction, including clearing it when there is no current Version.
CREATE FUNCTION record_catalog_acceptance_time() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.current_accepted_at := (
  SELECT rc.accepted_at FROM record_versions v
  JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
  WHERE v.organization=NEW.organization AND v.id=NEW.current_version_id
 );
 RETURN NEW;
END;
$$;
CREATE TRIGGER record_catalog_acceptance_time
BEFORE INSERT OR UPDATE OF current_version_id ON records
FOR EACH ROW EXECUTE FUNCTION record_catalog_acceptance_time();

-- -infinity sorts undated Records last and permits an indexed tuple seek even
-- when a page crosses from dated to undated Records. Bounds exclude undated rows.
CREATE INDEX records_catalog_accepted_order ON records
(organization,corpus_id,(coalesce(current_accepted_at,'-infinity'::timestamptz)) DESC,id COLLATE "C" DESC);
