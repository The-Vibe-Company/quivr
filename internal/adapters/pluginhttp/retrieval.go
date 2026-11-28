package pluginhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const maxPooledSearchRequestCap = 4 << 20

var (
	searchRequestBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 8192)
			return &b
		},
	}
	servedNullSuffix = []byte(`,"served":null}`)
	budgetFieldKey   = []byte(`,"budget":`)
)

// Retriever is the pinned plugin's retrieval Contribution as search calls
// it. Search judges each answer with the Contract Runner's rules
// (plugins.RetrievalSession) before it serves or returns anything.
type Retriever struct {
	Pin     *plugins.Pin
	breaker *circuitBreaker
}

var _ retrieval.Ranker = Retriever{}

// Manifest is the pinned plugin's manifest.
func (r Retriever) Manifest() *plugins.Manifest { return &r.Pin.Manifest }

// Configuration is the pin's validated plugin configuration.
func (r Retriever) Configuration() json.RawMessage { return r.Pin.Configuration }

func (r Retriever) endpointBreaker() *circuitBreaker {
	if r.breaker != nil {
		return r.breaker
	}
	if r.Pin == nil {
		return nil
	}
	return defaultBreakers.forEndpoint(r.Pin.Endpoint)
}

// marshalSearchRequest encodes request into wire bytes byte-identical to
// json.Marshal(request), reusing cached JSON encodings of immutable prior
// ServedRequest entries and a pooled output buffer. The returned release
// callback must be called once the caller is done reading the slice.
func marshalSearchRequest(request plugins.SearchRequest) ([]byte, func(), error) {
	if len(request.Served) == 0 {
		body, err := json.Marshal(request)
		return body, func() {}, err
	}

	var stackRaw [16][]byte
	raws := stackRaw[:0]
	if len(request.Served) > len(stackRaw) {
		raws = make([][]byte, 0, len(request.Served))
	}
	servedBytes := 2 // '[' and ']'
	for i, sr := range request.Served {
		if i > 0 {
			servedBytes++ // ','
		}
		raw, err := sr.MarshalCachedJSON()
		if err != nil {
			return nil, nil, err
		}
		raws = append(raws, raw)
		servedBytes += len(raw)
	}

	headReq := request
	headReq.Served = nil
	headReq.Budget = nil
	headBytes, err := json.Marshal(headReq)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.HasSuffix(headBytes, servedNullSuffix) {
		body, err := json.Marshal(request)
		return body, func() {}, err
	}

	var budgetBuf [96]byte
	budgetBytes := budgetBuf[:0]
	if request.Budget != nil {
		if math.IsInf(request.Budget.RemainingCostCents, 0) || math.IsNaN(request.Budget.RemainingCostCents) {
			return nil, nil, fmt.Errorf("json: unsupported value: %v", request.Budget.RemainingCostCents)
		}
		budgetBytes = append(budgetBytes, `{"remaining_time_ms":`...)
		budgetBytes = strconv.AppendInt(budgetBytes, int64(request.Budget.RemainingTimeMS), 10)
		budgetBytes = append(budgetBytes, `,"remaining_cost_cents":`...)
		abs := math.Abs(request.Budget.RemainingCostCents)
		fmtByte := byte('f')
		if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
			fmtByte = 'e'
		}
		fStart := len(budgetBytes)
		budgetBytes = strconv.AppendFloat(budgetBytes, request.Budget.RemainingCostCents, fmtByte, -1, 64)
		if fmtByte == 'e' {
			n := len(budgetBytes)
			if n-fStart >= 4 && budgetBytes[n-4] == 'e' && budgetBytes[n-3] == '-' && budgetBytes[n-2] == '0' {
				budgetBytes[n-2] = budgetBytes[n-1]
				budgetBytes = budgetBytes[:n-1]
			}
		}
		budgetBytes = append(budgetBytes, '}')
	}

	prefixLen := len(headBytes) - len("null}")
	total := prefixLen + servedBytes + 1
	if len(budgetBytes) > 0 {
		total += len(budgetFieldKey) + len(budgetBytes)
	}

	bp := searchRequestBufPool.Get().(*[]byte)
	buf := *bp
	if cap(buf) < total {
		buf = make([]byte, 0, total)
	} else {
		buf = buf[:0]
	}
	buf = append(buf, headBytes[:prefixLen]...)
	buf = append(buf, '[')
	for i, raw := range raws {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, raw...)
	}
	buf = append(buf, ']')
	if len(budgetBytes) > 0 {
		buf = append(buf, budgetFieldKey...)
		buf = append(buf, budgetBytes...)
	}
	buf = append(buf, '}')

	var released atomic.Bool
	release := func() {
		if released.Swap(true) {
			return
		}
		if cap(buf) <= maxPooledSearchRequestCap {
			*bp = buf[:0]
			searchRequestBufPool.Put(bp)
		}
	}
	return buf, release, nil
}

// Round posts one search round within ctx, which carries the profile's
// deadline. A terminal error envelope is content.ErrInvalid (the plugin
// refuses the query); an answer over max_response_bytes is
// retrieval.ErrPluginInvalid; a retryable error, a missing envelope or a
// transport failure is ErrUnavailable.
func (r Retriever) Round(ctx context.Context, request plugins.SearchRequest) ([]byte, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
		observe(r.Pin, request.OrganizationID, OpSearchRound, started, nil, err)
		return nil, err
	}
	b := r.endpointBreaker()
	var isProbe bool
	if b != nil {
		var allowed bool
		allowed, isProbe = b.Allow()
		if !allowed {
			observe(r.Pin, request.OrganizationID, OpSearchRound, started, nil, ErrCircuitOpen)
			return nil, ErrCircuitOpen
		}
	}
	body, release, err := marshalSearchRequest(request)
	if err != nil {
		if b != nil {
			b.ReleaseProbe(isProbe)
		}
		return nil, err
	}
	invokeCtx, cleanup := devhost.WithRequestRelease(ctx, release)
	defer cleanup()

	result, err := call.Invoke(invokeCtx, r.Pin, call.SearchRound, call.Bytes(body), nil, nil)
	if b != nil {
		b.Record(classifyRoundOutcome(ctx, r.Pin, result, err), isProbe)
	}
	observe(r.Pin, request.OrganizationID, OpSearchRound, started, result, err)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
