package postgres

// These read relations preserve the old reader vocabulary during resumable
// conversion. The compact branch synthesizes metadata; it never stores JSON
// per vector. Tuple predicates can use the existing canonical keys and new
// sequential interned keys. Legacy coverage remains authoritative until retired.
const compactArtifactRowsSQL = `SELECT o.organization,encode(e.artifact_sha256,'hex') AS id,
 'embedding-derivation_'||encode(sha256(convert_to(
 replace(replace(replace(replace(replace(array_to_json(ARRAY[o.organization,k.segment_id,sp.space_id,sg.derivation->>'model_input_sha256',f.producer])::text,
 '<','\u003c'),'>','\u003e'),'&','\u0026'),U&'\2028','\u2028'),U&'\2029','\u2029'),'UTF8')),'hex') AS derivation_id,
 k.segment_id,sp.space_id,
 jsonb_build_object(
 'embedding_artifact_id',encode(e.artifact_sha256,'hex'),
 'derivation_id','embedding-derivation_'||encode(sha256(convert_to(replace(replace(replace(replace(replace(array_to_json(ARRAY[o.organization,k.segment_id,sp.space_id,sg.derivation->>'model_input_sha256',f.producer])::text,'<','\u003c'),'>','\u003e'),'&','\u0026'),U&'\2028','\u2028'),U&'\2029','\u2029'),'UTF8')),'hex'),
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
 JOIN embedding_files f ON f.id=e.file_id`

const embeddingArtifactsRelation = `(SELECT a.organization,a.id,a.derivation_id,a.segment_id,a.space_id,a.metadata FROM embedding_artifacts a
 WHERE NOT EXISTS(SELECT 1 FROM storage_organizations o JOIN storage_segments k ON k.organization_id=o.id AND k.segment_id=a.segment_id
 JOIN storage_spaces sp ON sp.space_id=a.space_id JOIN compact_embeddings e ON (e.organization_id,e.segment_id,e.space_id)=(o.id,k.id,sp.id) WHERE o.organization=a.organization AND encode(e.artifact_sha256,'hex')=a.id)
 UNION ALL ` + compactArtifactRowsSQL + `)`

// embeddingCoverageForSegmentSQL bounds both storage branches before their
// joins. Outer join predicates cannot parameterize the joined UNION child,
// which otherwise scans compact coverage for every candidate segment. Arguments
// are trusted SQL expressions, never input values; callers supply LATERAL when
// referencing another FROM item. Legacy coverage remains authoritative.
func embeddingCoverageForSegmentSQL(organization, segment string) string {
	return `(SELECT organization,segment_id,generation_id,artifact_id,space_id FROM embedding_coverage
 WHERE organization=` + organization + ` AND segment_id=` + segment + `
 UNION ALL SELECT o.organization,k.segment_id,cc.generation_id,encode(e.artifact_sha256,'hex'),sp.space_id
 FROM storage_organizations o
 JOIN storage_segments k ON k.organization_id=o.id
 JOIN compact_embeddings e ON (e.organization_id,e.segment_id)=(k.organization_id,k.id)
 JOIN compact_embedding_coverage cc ON (cc.organization_id,cc.file_id)=(e.organization_id,e.file_id)
 JOIN storage_spaces sp ON sp.id=e.space_id
 WHERE o.organization=` + organization + ` AND k.segment_id=` + segment + `
 AND get_bit(cc.covered,e.ordinal)=1 AND NOT EXISTS(SELECT 1 FROM embedding_coverage old
 WHERE old.organization=o.organization AND old.segment_id=k.segment_id AND old.generation_id=cc.generation_id AND old.space_id=sp.space_id))`
}
