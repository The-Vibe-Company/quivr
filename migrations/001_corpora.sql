CREATE TABLE corpora (
 organization text NOT NULL,
 id text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 name text NOT NULL,
 retrieval jsonb NOT NULL,
 PRIMARY KEY (organization,id),
 UNIQUE (organization,request_key)
);
