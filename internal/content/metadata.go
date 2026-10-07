package content

import (
	"context"
	"strconv"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// ProjectionMetadata extracts only declared, correctly typed filter values.
func ProjectionMetadata(v Version, fields []corpus.Field) map[string]any {
	view := sourceView(v)
	out := map[string]any{}
	for _, f := range corpus.FilterFields(fields) {
		node, ok := fieldValue(view, f)
		if !ok {
			continue
		}
		value, ok := corpus.FilterValue(node, f.Type, true)
		if ok {
			out[f.Name] = value
		}
	}
	return out
}
func pointerValue(view any, pointer string) (any, bool) {
	tokens, ok := corpus.PointerTokens(pointer)
	if !ok {
		return nil, false
	}
	node := view
	for _, token := range tokens {
		switch current := node.(type) {
		case map[string]any:
			node, ok = current[token]
		case []any:
			i, err := strconv.Atoi(token)
			ok = err == nil && i >= 0 && i < len(current) && strconv.Itoa(i) == token
			if ok {
				node = current[i]
			}
		default:
			ok = false
		}
		if !ok {
			return nil, false
		}
	}
	return node, true
}

// MetadataWriter stores the typed values a generation actually projected,
// before coverage/route activation makes them visible to catalog readers.
type MetadataWriter interface {
	SaveProjectionMetadata(ctx context.Context, org, versionID, generationID string, values map[string]any) error
}
