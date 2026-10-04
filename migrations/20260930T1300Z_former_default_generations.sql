-- Former default generations (THE-787). When the deployment's vector spaces
-- change, the engine routes every Corpus without a route to the current
-- default generation, so it keeps its results until rebuilt, and a new
-- default carrying the registered spaces takes over for Corpora created
-- afterwards. default_until records when a generation stopped being the
-- default, so the objects of a Corpus rebuilt away from it are purged like
-- those of the active default. Generations deactivated before per-Corpus
-- routes existed stay NULL.
ALTER TABLE projection_generations ADD COLUMN default_until timestamptz;
