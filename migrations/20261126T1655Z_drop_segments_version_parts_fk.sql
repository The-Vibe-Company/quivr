-- Relax the text-only version_parts foreign key on segments so multimodal
-- region-only segments referencing Blob Parts (stored in verified_blobs) can
-- persist in PostgreSQL while keeping segments -> segmentations integrity.
ALTER TABLE public.segments DROP CONSTRAINT IF EXISTS segments_organization_version_id_part_key_fkey;
