-- Bounded provider-negotiated results survive activity/plugin restarts before
-- their complete immutable segmentation is published. Limits bound each page,
-- never the number of passages an accepted Version may produce.
CREATE TABLE ingestion_pages (
 organization text NOT NULL,
 version_id text NOT NULL,
 recipe text NOT NULL,
 spaces_key text NOT NULL,
 page_number integer NOT NULL CHECK (page_number >= 0),
 segments jsonb NOT NULL CHECK (jsonb_typeof(segments) = 'array'),
 next_cursor jsonb,
 PRIMARY KEY (organization, version_id, recipe, spaces_key, page_number)
);
