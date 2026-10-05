CREATE TABLE audit_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 actor text NOT NULL CHECK (octet_length(actor) <= 128),
 action text NOT NULL CHECK (octet_length(action) BETWEEN 1 AND 128),
 target_type text NOT NULL CHECK (octet_length(target_type) BETWEEN 1 AND 64),
 target_id text NOT NULL CHECK (octet_length(target_id) <= 512),
 organization text NOT NULL,
 outcome text NOT NULL CHECK (outcome IN ('accepted','refused')),
 request_id text NOT NULL CHECK (octet_length(request_id) <= 128),
 detail jsonb NOT NULL CHECK (octet_length(detail::text) <= 2048)
);
CREATE INDEX audit_events_organization_page ON audit_events(organization,id DESC);
CREATE INDEX audit_events_retention ON audit_events(occurred_at);
CREATE INDEX audit_events_actor ON audit_events(organization,actor,id DESC);
CREATE INDEX audit_events_action ON audit_events(organization,action,id DESC);
CREATE INDEX audit_events_target ON audit_events(organization,target_type,target_id,id DESC);

-- Runtime roles receive SELECT/INSERT only; the migration owner retains the
-- retention privilege. A runtime must not own the schema or these functions.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM PUBLIC;
CREATE FUNCTION guard_audit_events() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
 IF TG_OP = 'DELETE' AND current_setting('quivr.audit_pruning', true) = 'on'
    AND current_user = pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid = 'public.audit_events'::regclass)) THEN
  RETURN OLD;
 END IF;
 RAISE EXCEPTION 'audit events are append-only' USING ERRCODE = '42501';
END;
$$;
CREATE TRIGGER audit_events_append_only BEFORE UPDATE OR DELETE ON audit_events
 FOR EACH ROW EXECUTE FUNCTION guard_audit_events();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
 FOR EACH STATEMENT EXECUTE FUNCTION guard_audit_events();

-- The only supported removal path is a bounded retention pass. The runtime
-- may call it but cannot choose individual entries or change their contents.
CREATE FUNCTION prune_audit_events(retention_months integer, batch_size integer) RETURNS integer
 LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE removed integer;
BEGIN
 IF retention_months IS NULL OR batch_size IS NULL
    OR retention_months < 1 OR retention_months > 1200 OR batch_size < 1 OR batch_size > 10000 THEN
  RAISE EXCEPTION 'invalid audit retention or batch size';
 END IF;
 PERFORM set_config('quivr.audit_pruning', 'on', true);
 DELETE FROM public.audit_events WHERE id IN (
  SELECT id FROM public.audit_events
  WHERE occurred_at < clock_timestamp() - make_interval(months => retention_months)
  ORDER BY occurred_at, id LIMIT batch_size FOR UPDATE SKIP LOCKED
 );
 GET DIAGNOSTICS removed = ROW_COUNT;
 PERFORM set_config('quivr.audit_pruning', 'off', true);
 RETURN removed;
END;
$$;

-- Database operators grant this privilege to their worker role explicitly.
REVOKE EXECUTE ON FUNCTION public.prune_audit_events(integer,integer) FROM PUBLIC;
