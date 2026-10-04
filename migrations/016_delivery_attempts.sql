-- Append-only admission facts: one row per admitted transport attempt,
-- committed with the Delivery's move to delivering before any network I/O.
CREATE TABLE delivery_attempts (
 organization text NOT NULL,
 id text NOT NULL,
 delivery_id text NOT NULL,
 number integer NOT NULL CHECK(number>=1),
 admitted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,id),
 UNIQUE(organization,delivery_id,number),
 FOREIGN KEY(organization,delivery_id) REFERENCES deliveries(organization,id)
);
-- Append-only outcome facts: at most one per attempt, written after I/O (or
-- as unknown when a worker lost the attempt). No receiver body, signature or
-- secret is stored; error text is bounded and URL-free.
CREATE TABLE delivery_attempt_outcomes (
 organization text NOT NULL,
 attempt_id text NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('acknowledged','retryable_error','permanent_error','unknown')),
 http_status integer CHECK(http_status BETWEEN 100 AND 599),
 error_code text NOT NULL DEFAULT '',
 error_message text NOT NULL DEFAULT '' CHECK(length(error_message)<=200),
 completed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,attempt_id),
 FOREIGN KEY(organization,attempt_id) REFERENCES delivery_attempts(organization,id)
);
CREATE FUNCTION forbid_attempt_fact_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'delivery attempt facts are append-only';
END $$;
CREATE TRIGGER delivery_attempts_append_only BEFORE UPDATE OR DELETE ON delivery_attempts
 FOR EACH ROW EXECUTE FUNCTION forbid_attempt_fact_mutation();
CREATE TRIGGER delivery_attempt_outcomes_append_only BEFORE UPDATE OR DELETE ON delivery_attempt_outcomes
 FOR EACH ROW EXECUTE FUNCTION forbid_attempt_fact_mutation();
-- Latest recorded attempt outcome on the logical Delivery, so retry and
-- exhaustion policy can distinguish a permanent failure without re-reading
-- attempts. Empty until the first outcome.
ALTER TABLE deliveries ADD COLUMN last_outcome text NOT NULL DEFAULT ''
 CHECK(last_outcome IN ('','acknowledged','retryable_error','permanent_error','unknown'));
