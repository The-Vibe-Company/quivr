package postgres

import (
	"encoding/json"
	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Packed tuples carry the canonical numeric keys and digests. Metadata is
// reconstructed for callers; derivation identity uses Go's canonical encoder.
const embeddingArtifactsRelation = `(SELECT o.organization,encode(e.artifact_sha256,'hex') AS id, k.segment_id,sp.space_id,
 jsonb_build_object(
 'embedding_artifact_id',encode(e.artifact_sha256,'hex'),
 'organization_id',o.organization,'corpus_id',f.corpus_id,'version_id',f.version_id,'segment_id',k.segment_id,
 'part_key',sg.part_key,'segmentation_id',f.segmentation_id,'segmentation_recipe',f.recipe,
 'normalized_content_sha256',sg.derivation->>'normalized_content_sha256','slice_sha256',sg.text_sha256,
 'input_sha256',sg.derivation->>'model_input_sha256','input_bytes',octet_length(coalesce(sg.derivation->>'model_input','')),
 'vector_space_id',sp.space_id,'producer',f.producer,
 'payload',jsonb_build_object('key',encode(sha256(convert_to(o.organization,'UTF8')),'hex')||'/sha256/'||encode(e.vector_sha256,'hex'),'sha256',encode(e.vector_sha256,'hex'),'size',f.dimensions*4),
 'vector_ordinal',e.ordinal,
 'vector_file',jsonb_build_object('id',f.id,'organization',o.organization,'corpus',f.corpus_id,'version',f.version_id,'segmentation',f.segmentation_id,'recipe',f.recipe,'space',sp.space_id,'producer',f.producer,'dimensions',f.dimensions,'rows',f.row_count,'presence',encode(f.presence,'base64'),'blob',jsonb_build_object('key',f.object_key,'sha256',encode(f.sha256,'hex'),'size',f.byte_length))
 ) AS metadata
 FROM compact_embeddings e
 JOIN storage_organizations o ON o.id=e.organization_id
 JOIN storage_segments k ON (k.organization_id,k.id)=(e.organization_id,e.segment_id)
 JOIN segments sg ON (sg.organization,sg.id)=(o.organization,k.segment_id)
 JOIN storage_spaces sp ON sp.id=e.space_id
 JOIN embedding_files f ON f.id=e.file_id)`

// Arguments are trusted SQL expressions; callers use LATERAL for another FROM
// item's keys so coverage work remains bounded to the requested segments.
// Keep numeric-key seeks behind their parent lookups even while small-table
// statistics still describe an empty installation.
func embeddingCoverageForSegmentSQL(organization, segment string) string {
	return `(SELECT o.organization,k.segment_id,cc.generation_id,encode(e.artifact_sha256,'hex') AS artifact_id,sp.space_id
 FROM storage_organizations o
 CROSS JOIN LATERAL (SELECT k.organization_id,k.id,k.segment_id FROM storage_segments k
  WHERE k.organization_id=o.id AND k.segment_id=` + segment + ` OFFSET 0) k
 CROSS JOIN LATERAL (SELECT e.* FROM compact_embeddings e
  WHERE (e.organization_id,e.segment_id)=(k.organization_id,k.id) OFFSET 0) e
 JOIN compact_embedding_coverage cc ON (cc.organization_id,cc.file_id)=(e.organization_id,e.file_id)
 JOIN storage_spaces sp ON sp.id=e.space_id
 WHERE o.organization=` + organization + ` AND k.segment_id=` + segment + `
 AND get_bit(cc.covered,e.ordinal)=1)`
}

func decodeEmbedding(raw []byte) (content.Embedding, error) {
	var e content.Embedding
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, err
	}
	e.DerivationID = content.StableID("embedding-derivation", e.Organization, e.SegmentID, e.SpaceID, e.InputSHA, e.Producer)
	return e, nil
}
