package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
)

func requestMatches(previous, digest, canonical []byte) bool {
	sum := sha256.Sum256(canonical)
	if len(digest) > 0 {
		return bytes.Equal(digest, sum[:])
	}
	return bytes.Equal(previous, canonical)
}

// Full request identity is hashed before removing transport-only fields from
// the one durable work input. Required source/content/provenance remain intact.
func importRequestStorage(canonical []byte, retainDetail bool) (copy, receipt, execution []byte) {
	if retainDetail {
		return canonical, canonical, canonical
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &fields)
	delete(fields, "idempotency_key")
	delete(fields, "source_revision")
	delete(fields, "source_position")
	execution, _ = json.Marshal(fields)
	return []byte{}, []byte("{}"), execution
}
