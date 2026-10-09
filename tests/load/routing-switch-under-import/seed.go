package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seed(ctx context.Context, pool *pgxpool.Pool, n int) {
	statements := []string{
		`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) SELECT 'scale','c'||i,'c'||i,'{}','Corpus '||i,'{}' FROM generate_series(0,99) i`,
		`INSERT INTO projection_generations(id,collection,profile_version,space_id,spaces,spaces_projected,ingestion_routing,source_namespace_projected,metadata_projected,item_keywords_projected) SELECT 'g'||i,'RoutingProof'||i,'proof','example.hash_embedder.small@1',spaces,true,'{"default":"example.hash_embedder","routes":{}}',true,true,true FROM generate_series(0,99) i CROSS JOIN projection_generations g WHERE g.active`,
		`INSERT INTO corpus_projection_routes SELECT 'scale','c'||i,'g'||i,0 FROM generate_series(0,99) i`,
		`INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length) VALUES('scale','text','proof/text','proof',1)`,
		`INSERT INTO storage_organizations(organization) VALUES('scale')`,
		`INSERT INTO storage_spaces(space_id) SELECT id FROM vector_spaces WHERE id IN ('example.hash_embedder.small@1','example.hash_embedder.large@1','certified.ingestion-valid.small@1')`,
		`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id) SELECT 'scale','r'||lpad(i::text,10,'0'),'c'||(i%100),'proof',i::text,'v'||lpad(i::text,10,'0'),'v'||lpad(i::text,10,'0') FROM generate_series(1,$1::int) i`,
		`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,source_media_type) SELECT organization,id,'1','proof',current_version_id,1,'','{}','text/plain' FROM records WHERE organization='scale'`,
		`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,enrichment_state) SELECT organization,current_version_id,id,'1','proof',1,'','text','text','{}',true,'idle' FROM records WHERE organization='scale'`,
		`INSERT INTO version_parts(organization,version_id,part_key,role,blob_id) SELECT organization,id,'body','body','text' FROM record_versions WHERE organization='scale'`,
		`INSERT INTO segmentations(organization,id,version_id,recipe,digest) SELECT organization,'s'||p.owner||substring(id from 2),id,'plugin:'||CASE WHEN p.owner='a' THEN 'example.hash_embedder' ELSE 'certified.ingestion-valid' END||'@0.1.0','proof' FROM record_versions CROSS JOIN (VALUES('a'),('b')) p(owner) WHERE organization='scale'`,
		`INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256) SELECT organization,id,id,version_id,'body',0,1,'proof' FROM segmentations WHERE organization='scale'`,
		`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id,role) SELECT s.organization,s.version_id,'g'||(substring(s.version_id from 2)::bigint%100),s.id,CASE WHEN s.recipe LIKE 'plugin:example.%' THEN 'served' ELSE 'evaluation' END FROM segmentations s WHERE s.organization='scale'`,
		`INSERT INTO storage_segments(organization_id,segment_id) SELECT o.id,s.id FROM segments s JOIN storage_organizations o ON o.organization=s.organization WHERE s.organization='scale'`,
		`INSERT INTO embedding_files(organization_id,version_id,segmentation_id,space_id,corpus_id,recipe,producer,object_key,sha256,byte_length,dimensions,row_count,presence) SELECT o.id,s.version_id,s.id,sp.id,'c'||(substring(s.version_id from 2)::bigint%100),s.recipe,s.recipe,'proof/vector',decode(repeat('00',32),'hex'),16,4,1,'\x01' FROM segmentations s JOIN storage_organizations o ON o.organization=s.organization CROSS JOIN storage_spaces sp WHERE s.organization='scale' AND (s.recipe LIKE 'plugin:example.%' AND sp.space_id LIKE 'example.%' OR s.recipe LIKE 'plugin:certified.%' AND sp.space_id LIKE 'certified.%')`,
		`INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256) SELECT f.organization_id,s.id,f.space_id,f.id,0,decode(repeat('00',32),'hex'),sha256(convert_to(f.id::text,'UTF8')) FROM embedding_files f JOIN storage_segments s ON (s.organization_id,s.segment_id)=(f.organization_id,f.segmentation_id)`,
		`INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered) SELECT organization_id,id,'g'||(substring(version_id from 2)::bigint%100),'\x01' FROM embedding_files`,
		`ANALYZE`,
	}
	for i, q := range statements {
		var err error
		if i == 6 {
			_, err = pool.Exec(ctx, q, n)
		} else {
			_, err = pool.Exec(ctx, q)
		}
		if err != nil {
			panic(fmt.Errorf("seed statement %d: %w", i, err))
		}
		fmt.Printf("seed statement %d complete\n", i)
	}
}
