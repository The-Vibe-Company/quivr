-- Search can filter on Source Namespace (THE-769). A generation whose objects
-- all carry their Record's Source Namespace is marked projected. Existing
-- generations hold objects written without it, so they stay false and a
-- filtered search on them is refused until the Corpus is rebuilt. The default
-- generation of a fresh install and every new rebuild target are projected.
ALTER TABLE projection_generations ADD COLUMN source_namespace_projected boolean NOT NULL DEFAULT false;
