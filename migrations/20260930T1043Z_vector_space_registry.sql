-- The vector space registry and named spaces per generation (THE-776).
-- A registered space has one owner: an ingestion plugin, or the engine itself
-- (empty owner) for the built-in E5 space. api, worker and migrate register
-- the deployment's spaces at startup and set each one's role: served,
-- evaluation, or retired when the deployment no longer enables it.
ALTER TABLE vector_spaces
  ADD COLUMN name text NOT NULL DEFAULT '',
  ADD COLUMN version text NOT NULL DEFAULT '',
  ADD COLUMN owner_plugin_id text NOT NULL DEFAULT '',
  ADD COLUMN owner_plugin_version text NOT NULL DEFAULT '',
  ADD COLUMN model text NOT NULL DEFAULT '',
  ADD COLUMN dimensions integer NOT NULL DEFAULT 0,
  ADD COLUMN metric text NOT NULL DEFAULT 'cosine',
  ADD COLUMN indexes text[] NOT NULL DEFAULT '{text}',
  ADD COLUMN query_modalities text[] NOT NULL DEFAULT '{text}',
  ADD COLUMN role text NOT NULL DEFAULT 'retired' CHECK (role IN ('served','evaluation','retired'));
-- A generation's objects carry a named vector for each of its spaces
-- ([{"id","metric"}], the served one first). Existing generations were built
-- with one vector and no lexical text: they stay unprojected, and a request
-- for a named space on them is refused until the Corpus is rebuilt.
ALTER TABLE projection_generations
  ADD COLUMN spaces jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN spaces_projected boolean NOT NULL DEFAULT false;
-- A segment holds at most one vector per space in a generation.
ALTER TABLE embedding_coverage ADD COLUMN space_id text NOT NULL DEFAULT '';
UPDATE embedding_coverage ec SET space_id=a.space_id FROM embedding_artifacts a WHERE a.organization=ec.organization AND a.id=ec.artifact_id;
ALTER TABLE embedding_coverage DROP CONSTRAINT embedding_coverage_pkey;
ALTER TABLE embedding_coverage ADD PRIMARY KEY (organization,segment_id,generation_id,space_id);
