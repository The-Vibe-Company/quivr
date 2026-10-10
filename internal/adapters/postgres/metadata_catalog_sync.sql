-- Epoch values are materialized at publication, not cast in an index expression.
-- PostgreSQL's negative years count BC from -1; RFC3339 uses astronomical year 0.
CREATE FUNCTION projection_metadata_filter_epoch(input text) RETURNS bigint
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
    parts text[];
    year_number integer;
    offset_seconds integer := 0;
    instant timestamp;
BEGIN
    parts := regexp_match(input, '^(-?[0-9]{4,5})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2}(\.[0-9]{1,9})?)(Z|[+-][0-9]{2}:[0-9]{2})$');
    IF parts IS NULL THEN RETURN NULL; END IF;
    year_number := parts[1]::integer;
    IF year_number <= 0 THEN year_number := year_number - 1; END IF;
    instant := make_timestamp(year_number,parts[2]::integer,parts[3]::integer,
                              parts[4]::integer,parts[5]::integer,parts[6]::double precision);
    IF parts[8] <> 'Z' THEN
        offset_seconds := (substring(parts[8],2,2)::integer*60 + substring(parts[8],5,2)::integer)*60;
        IF left(parts[8],1) = '-' THEN offset_seconds := -offset_seconds; END IF;
    END IF;
    RETURN floor(extract(epoch FROM instant)*1000)::bigint - offset_seconds::bigint*1000;
EXCEPTION WHEN datetime_field_overflow OR invalid_datetime_format OR numeric_value_out_of_range THEN
    RETURN NULL;
END $$;

CREATE FUNCTION projection_metadata_filter_sync() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    source_corpus text;
    indexes_ready boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM projection_metadata_filter_values
        WHERE (organization,version_id,generation_id)=(OLD.organization,OLD.version_id,OLD.generation_id);
        RETURN NULL;
    END IF;
    SELECT count(*)=3 INTO indexes_ready FROM pg_index
    WHERE indexrelid IN (to_regclass('projection_metadata_filter_source'),
                        to_regclass('projection_metadata_filter_values_lookup'),
                        to_regclass('projection_metadata_filter_dates_lookup')) AND indisvalid;
    IF NOT indexes_ready THEN
        -- Old binaries also leave a durable pending row during concurrent builds.
        UPDATE projection_metadata SET filter_indexed=false
        WHERE (organization,version_id,generation_id)=(NEW.organization,NEW.version_id,NEW.generation_id);
        RETURN NULL;
    END IF;
    SELECT corpus_id INTO source_corpus FROM (
        SELECT r.corpus_id FROM accepted_revisions a JOIN records r
            ON (r.organization,r.id)=(a.organization,a.record_id)
            WHERE a.organization=NEW.organization AND a.version_id=NEW.version_id
        UNION ALL
        SELECT r.corpus_id FROM record_versions v JOIN records r
            ON (r.organization,r.id)=(v.organization,v.record_id)
            WHERE v.organization=NEW.organization AND v.id=NEW.version_id
    ) source LIMIT 1;
    DELETE FROM projection_metadata_filter_values
        WHERE (organization,version_id,generation_id)=(NEW.organization,NEW.version_id,NEW.generation_id);
    IF source_corpus IS NOT NULL THEN
        INSERT INTO projection_metadata_filter_values
            (organization,corpus_id,generation_id,version_id,field,value,date_epoch_ms)
        SELECT DISTINCT NEW.organization,source_corpus,NEW.generation_id,NEW.version_id,
            entry.key,member.value,
            CASE WHEN jsonb_typeof(member.value)='string'
                THEN projection_metadata_filter_epoch(member.value #>> '{}') END
        FROM jsonb_each(CASE WHEN jsonb_typeof(NEW.data)='object' THEN NEW.data ELSE '{}'::jsonb END) entry CROSS JOIN LATERAL
            jsonb_array_elements(CASE WHEN jsonb_typeof(entry.value)='array'
                THEN entry.value ELSE jsonb_build_array(entry.value) END) member
        WHERE jsonb_typeof(member.value) IN ('string','number','boolean');
    END IF;
    UPDATE projection_metadata SET filter_indexed=true
        WHERE (organization,version_id,generation_id)=(NEW.organization,NEW.version_id,NEW.generation_id);
    RETURN NULL;
END $$;

-- Updating the marker does not recurse. Source-row locking serializes repeated
-- publication, bootstrap, replacement and deletion with their derived entries.
CREATE TRIGGER projection_metadata_filter_sync AFTER INSERT OR UPDATE OF data OR DELETE
ON projection_metadata FOR EACH ROW EXECUTE FUNCTION projection_metadata_filter_sync();
