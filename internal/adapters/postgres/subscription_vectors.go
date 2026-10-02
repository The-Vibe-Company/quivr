package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
)

func (s ContentStore) SubscriptionEmbeddings(ctx context.Context, org, corpusID, versionID string) (content.Generation, []string, int, error) {
	generation, err := s.Generation(ctx, org, corpusID)
	if err != nil {
		return generation, nil, 0, err
	}
	var recipe string
	err = s.Pool.QueryRow(ctx, `SELECT ss.recipe FROM projection_coverage pc JOIN segmentations ss ON (ss.organization,ss.id)=(pc.organization,pc.segmentation_id) WHERE pc.organization=$1 AND pc.version_id=$2 AND pc.generation_id=$3 AND pc.role='served'`, org, versionID, generation.ID).Scan(&recipe)
	if errors.Is(err, pgx.ErrNoRows) {
		return generation, nil, 0, nil
	}
	if err != nil {
		return generation, nil, 0, err
	}
	generation.SpaceID = generation.ServedFor(content.PluginOfRecipe(recipe))
	rows, err := s.Pool.Query(ctx, `SELECT ea.metadata
FROM projection_coverage pc
JOIN records r ON r.organization=pc.organization AND r.corpus_id=$2
JOIN record_versions v ON v.organization=r.organization AND v.record_id=r.id AND v.id=pc.version_id
JOIN segments sg ON (sg.organization,sg.segmentation_id)=(pc.organization,pc.segmentation_id)
LEFT JOIN embedding_coverage ec ON ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=pc.generation_id AND ec.space_id=$5
LEFT JOIN embedding_artifacts ea ON ea.organization=ec.organization AND ea.id=ec.artifact_id
WHERE pc.organization=$1 AND pc.version_id=$3 AND pc.generation_id=$4 AND pc.role='served'
ORDER BY sg.part_key,sg.start_offset,sg.id`, org, corpusID, versionID, generation.ID, generation.SpaceID)
	if err != nil {
		return generation, nil, 0, err
	}
	defer rows.Close()
	var derivations []string
	total := 0
	for rows.Next() {
		var metadata []byte
		if err := rows.Scan(&metadata); err != nil {
			return generation, nil, 0, err
		}
		total++
		if len(metadata) == 0 {
			continue
		}
		var artifact content.Embedding
		if err := json.Unmarshal(metadata, &artifact); err != nil {
			return generation, nil, 0, err
		}
		derivations = append(derivations, artifact.DerivationID)
	}
	return generation, derivations, total, rows.Err()
}
