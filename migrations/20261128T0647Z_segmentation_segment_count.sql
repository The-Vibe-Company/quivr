-- segmentation_segment_count
-- Expand only: keep the previous binary compatible. See docs/agents/migrations.md.
ALTER TABLE segmentations ADD COLUMN segment_count integer;
