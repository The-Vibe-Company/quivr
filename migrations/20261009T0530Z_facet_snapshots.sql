-- Each Corpus's unfiltered facet counts (THE-1387). Fast facet reads serve
-- them with taken_at as their age and refresh them under refreshing_until;
-- exact reads never use them. data is NULL until a first refresh stores it.
CREATE TABLE facet_snapshots (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    generation_id text NOT NULL DEFAULT '',
    data jsonb,
    taken_at timestamptz,
    duration_ms bigint NOT NULL DEFAULT 0,
    refreshing_until timestamptz,
    PRIMARY KEY (organization, corpus_id)
);
