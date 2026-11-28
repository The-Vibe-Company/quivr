package weaviate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/keywords"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const itemKind = "itemKind"
const segmentKind = "segment"
const passageText = "passageText"
const maxItemPassages = 2400

const (
	// Bound text-based passage selection to the top items that can reach the
	// caller, plus modest headroom for candidates replacing entries rejected
	// during hydration. Tail items only need one valid segment for fusion and fallback.
	maxPassageSelectionItems    = retrieval.MaxLimit
	maxPassageSelectionHeadroom = 10
	minTopKPassageScan          = 256
	maxTopKPassageScan          = 512
	passagesPerTopItem          = 16
	maxTailPassageScan          = 512
	passagesPerTailItem         = 4
	missingPassageConcurrency   = 8
	minDensePassageScan         = 64
	maxDensePassageScan         = maxItemPassages

	// Minimum lexical information density for a substantive passage. Shorter
	// boilerplate fragments (headers, footers, page markers) rank below
	// substantive passages when both match (or neither matches) the query.
	minSubstantiveTerms = 8
	minSubstantiveRunes = 40

	// Within-item passage BM25 parameters. Flooring substantive passage length
	// keeps short notices from exploiting BM25 length normalization against
	// richer paragraphs.
	passageBM25K1       = 1.2
	passageBM25B        = 0.4
	passageBM25MinTerms = 24.0
)

func itemProperty(f corpus.Field) string {
	b, _ := json.Marshal(f)
	return "k_" + content.Hash(b)[:32]
}

// analyzedProperty stores the field's keyword copy written by its analyzer.
func analyzedProperty(f corpus.Field, a keywords.Analyzer) string {
	return itemProperty(f) + "_" + a.Property
}

// fieldAnalyzers lists the distinct analyzers fields name, in field order.
func fieldAnalyzers(fields []corpus.Field) []keywords.Analyzer {
	var out []keywords.Analyzer
	seen := map[string]bool{}
	for _, f := range fields {
		if a, ok := keywords.Lookup(f.Analyzer); ok && !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) hasProperties(collection string, names []string) bool {
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	if s.metadataProperties == nil {
		return false
	}
	prefix := collection + "/"
	for _, name := range names {
		if !s.metadataProperties[prefix+name] {
			return false
		}
	}
	return true
}

func (s *Store) ensurePropertiesLazy(ctx context.Context, collection string, names []string, build func() []map[string]any) error {
	if s.hasProperties(collection, names) {
		return nil
	}
	return s.ensureProperties(ctx, collection, build())
}

func (s *Store) ensureItemSchema(ctx context.Context, g content.Generation) error {
	if !g.ItemKeywordsProjected {
		return nil
	}
	itemFields := content.ItemFields(g.Fields)
	names := make([]string, 0, 4+len(itemFields)*2)
	names = append(names, itemKind, "recordId", embeddingRevision, passageText)
	fieldProps := make([]string, len(itemFields))
	for i, f := range itemFields {
		prop := itemProperty(f)
		fieldProps[i] = prop
		names = append(names, prop)
		if a, ok := keywords.Lookup(f.Analyzer); ok {
			names = append(names, prop+"_"+a.Property)
		}
	}
	if s.hasProperties(g.Collection, names) {
		return nil
	}
	props := make([]map[string]any, 0, len(names))
	props = append(props,
		filterable(itemKind),
		filterable("recordId"),
		embeddingRevisionProperty(),
		map[string]any{"name": passageText, "dataType": []string{"text"}, "indexSearchable": false, "indexFilterable": false},
	)
	for i, f := range itemFields {
		prop := fieldProps[i]
		props = append(props, searchable(prop))
		if a, ok := keywords.Lookup(f.Analyzer); ok {
			props = append(props, searchable(prop+"_"+a.Property))
		}
	}
	return s.ensureProperties(ctx, g.Collection, props)
}

func (s *Store) missingProperties(collection string, props []map[string]any) []map[string]any {
	if s.metadataProperties == nil {
		s.metadataProperties = map[string]bool{}
	}
	prefix := collection + "/"
	var missing []map[string]any
	for _, p := range props {
		if !s.metadataProperties[prefix+p["name"].(string)] {
			missing = append(missing, p)
		}
	}
	return missing
}

func (s *Store) collectionSchemaLock(collection string) *semaphore.Weighted {
	if s.schemaSync == nil {
		s.schemaSync = map[string]*semaphore.Weighted{}
	}
	sem := s.schemaSync[collection]
	if sem == nil {
		sem = semaphore.NewWeighted(1)
		s.schemaSync[collection] = sem
	}
	return sem
}

// Reconcile additive schemas once per process, including concurrent creation.
func (s *Store) ensureProperties(ctx context.Context, collection string, props []map[string]any) error {
	s.metadataMu.Lock()
	missing := s.missingProperties(collection, props)
	if len(missing) == 0 {
		s.metadataMu.Unlock()
		return nil
	}
	sem := s.collectionSchemaLock(collection)
	s.metadataMu.Unlock()

	if err := sem.Acquire(ctx, 1); err != nil {
		return err
	}
	defer sem.Release(1)

	s.metadataMu.Lock()
	missing = s.missingProperties(collection, props)
	s.metadataMu.Unlock()
	if len(missing) == 0 {
		return nil
	}

	var schema struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	if _, err := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); err != nil {
		return err
	}
	present := make(map[string]bool, len(schema.Properties))
	for _, p := range schema.Properties {
		present[p.Name] = true
	}
	prefix := collection + "/"
	for _, p := range missing {
		name := p["name"].(string)
		if !present[name] {
			if _, err := s.call(ctx, "POST", "/v1/schema/"+collection+"/properties", p, nil); err != nil {
				schema.Properties = nil
				if _, readErr := s.call(ctx, "GET", "/v1/schema/"+collection, nil, &schema); readErr != nil {
					return readErr
				}
				found := false
				for _, p := range schema.Properties {
					if p.Name == name {
						found = true
					}
				}
				if !found {
					return err
				}
			}
		}
		s.metadataMu.Lock()
		s.metadataProperties[prefix+name] = true
		s.metadataMu.Unlock()
	}
	return nil
}

type itemRow struct {
	SegmentID    string `json:"segmentId"`
	GenerationID string `json:"generationId"`
	VersionID    string `json:"versionId"`
	Text         string `json:"passageText"`
	Additional   struct {
		Score    *string  `json:"score"`
		Distance *float64 `json:"distance"`
	} `json:"_additional"`
	scoreVal    float64
	distanceVal float64
	hasScore    bool
	hasDistance bool
}

func (r itemRow) candidate(dense bool) content.Candidate {
	score := 0.0
	if dense {
		if r.hasDistance {
			score = 1 - r.distanceVal
		} else if r.Additional.Distance != nil {
			score = 1 - *r.Additional.Distance
		} else if r.hasScore {
			score = r.scoreVal
		} else if r.Additional.Score != nil {
			score, _ = strconv.ParseFloat(*r.Additional.Score, 64)
		}
	} else {
		if r.hasScore {
			score = r.scoreVal
		} else if r.Additional.Score != nil {
			score, _ = strconv.ParseFloat(*r.Additional.Score, 64)
		}
	}
	return content.Candidate{SegmentID: r.SegmentID, GenerationID: r.GenerationID, VersionID: r.VersionID, Score: score}
}

var queryBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4<<10)
		return &b
	},
}

const maxPooledQueryCap = 64 << 10

func buildSearchRowsRequestBody(ctx context.Context, collection string, branch searchBranch, where filter, order searchOrder, limit int, item, text bool) []byte {
	bufPtr := queryBufPool.Get().(*[]byte)
	qb := (*bufPtr)[:0]
	est := len("{Get{(") + len(collection) + branch.encodedLen() + order.encodedLen() + where.encodedLen() + 128
	if cap(qb) < est {
		qb = make([]byte, 0, est)
	}
	qb = append(qb, "{Get{"...)
	qb = append(qb, collection...)
	qb = append(qb, '(')
	if tenant := tenantFrom(ctx); tenant != "" {
		qb = append(qb, "tenant:"...)
		qb = appendJSONString(qb, tenant)
		qb = append(qb, ',')
	}
	qb = branch.appendGraphQL(qb)
	qb = order.appendGraphQL(qb)
	if where.operator != 0 {
		qb = append(qb, "where:"...)
		qb = where.appendGraphQL(qb)
		qb = append(qb, ',')
	}
	qb = append(qb, "limit:"...)
	qb = strconv.AppendInt(qb, int64(limit), 10)
	qb = append(qb, "){segmentId generationId"...)
	if item {
		qb = append(qb, " versionId"...)
	}
	if text {
		qb = append(qb, ' ')
		qb = append(qb, passageText...)
	}
	qb = append(qb, " _additional{score distance}}}}"...)

	body := make([]byte, 0, len(qb)+len(`{"query":""}`)+32)
	body = append(body, `{"query":`...)
	body = appendJSONString(body, bytesAsString(qb))
	body = append(body, '}')

	if cap(qb) <= maxPooledQueryCap {
		*bufPtr = qb[:0]
		queryBufPool.Put(bufPtr)
	}
	return body
}

func (s *Store) searchRows(ctx context.Context, collection string, branch searchBranch, where filter, order searchOrder, limit int, item, text bool) ([]itemRow, error) {
	responseLimit := 2 << 20
	if text {
		// The bounded window can carry more text than a 128-row page.
		responseLimit = 16 << 20
	}
	if s.grpc != nil {
		fields := make([]string, 0, 4)
		fields = append(fields, "segmentId", "generationId")
		if item {
			fields = append(fields, "versionId")
		}
		if text {
			fields = append(fields, passageText)
		}
		return s.grpcSearchRows(ctx, collection, branch, where, order, limit, fields, responseLimit, true)
	}
	body := buildSearchRowsRequestBody(ctx, collection, branch, where, order, limit, item, text)
	client := s.Client
	if _, bounded := ctx.Deadline(); bounded && client.Timeout != 0 {
		// Search queries are already bounded by their caller. Keep the shared
		// transport and leave the client used for projection writes alone.
		c := *client
		c.Timeout = 0
		client = &c
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.Endpoint+"/v1/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, s.safeError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("projection connection unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("projection request failed")
	}
	limited := &io.LimitedReader{R: res.Body, N: int64(responseLimit)}
	rows, err := decodeItemRows(limited, collection, item, text, limit)
	if err != nil && limited.N == 0 {
		return nil, errResponseLimit
	}
	if err != nil {
		return nil, s.safeError(err)
	}
	return rows, nil
}

// Streaming typed GraphQL response decoder backed by pooled read buffers.

const (
	itemStreamBufSize    = 32 << 10
	maxStreamScratchCap  = 64 << 10
	maxStreamNesting     = 1024
	errInvalidJSONSyntax = "invalid projection JSON response"
)

type jsonStream struct {
	r       io.Reader
	buf     []byte
	pos     int
	end     int
	eof     bool
	err     error
	scratch []byte
}

var itemStreamPool = sync.Pool{
	New: func() any {
		return &jsonStream{
			buf:     make([]byte, itemStreamBufSize),
			scratch: make([]byte, 0, 256),
		}
	},
}

func acquireJSONStream(r io.Reader) *jsonStream {
	s := itemStreamPool.Get().(*jsonStream)
	s.r = r
	s.pos = 0
	s.end = 0
	s.eof = false
	s.err = nil
	s.scratch = s.scratch[:0]
	return s
}

func releaseJSONStream(s *jsonStream) {
	s.r = nil
	s.err = nil
	s.pos = 0
	s.end = 0
	s.eof = false
	if cap(s.scratch) > maxStreamScratchCap {
		s.scratch = make([]byte, 0, 256)
	} else {
		s.scratch = s.scratch[:0]
	}
	itemStreamPool.Put(s)
}

func (s *jsonStream) fill() bool {
	if s.eof || s.err != nil {
		return false
	}
	s.pos = 0
	s.end = 0
	for {
		n, err := s.r.Read(s.buf)
		if n > 0 {
			s.end = n
			if err == io.EOF {
				s.eof = true
			} else if err != nil {
				s.err = err
			}
			return true
		}
		if err == io.EOF {
			s.eof = true
			return false
		}
		if err != nil {
			s.err = err
			return false
		}
	}
}

func (s *jsonStream) eofErr() error {
	if s.err != nil {
		return s.err
	}
	return io.ErrUnexpectedEOF
}

func (s *jsonStream) peek() (byte, error) {
	for {
		for s.pos < s.end {
			c := s.buf[s.pos]
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				return c, nil
			}
			s.pos++
		}
		if !s.fill() {
			return 0, s.eofErr()
		}
	}
}

func (s *jsonStream) nextByte() (byte, error) {
	if s.pos >= s.end {
		if !s.fill() {
			return 0, s.eofErr()
		}
	}
	b := s.buf[s.pos]
	s.pos++
	return b, nil
}

func (s *jsonStream) expectByte(want byte) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c != want {
		return errors.New(errInvalidJSONSyntax)
	}
	s.pos++
	return nil
}

func (s *jsonStream) expectLiteral(lit string) error {
	for i := 0; i < len(lit); i++ {
		b, err := s.nextByte()
		if err != nil {
			return err
		}
		if b != lit[i] {
			return errors.New(errInvalidJSONSyntax)
		}
	}
	return nil
}

func parseHex4(s *jsonStream) (rune, error) {
	var r rune
	for range 4 {
		b, err := s.nextByte()
		if err != nil {
			return 0, err
		}
		var d rune
		switch {
		case b >= '0' && b <= '9':
			d = rune(b - '0')
		case b >= 'a' && b <= 'f':
			d = rune(b-'a') + 10
		case b >= 'A' && b <= 'F':
			d = rune(b-'A') + 10
		default:
			return 0, errors.New(errInvalidJSONSyntax)
		}
		r = (r << 4) | d
	}
	return r, nil
}

func (s *jsonStream) readStringBytes() ([]byte, error) {
	if err := s.expectByte('"'); err != nil {
		return nil, err
	}
	// Fast path: unescaped string contained completely inside the current buffer.
	start := s.pos
	buf := s.buf[:s.end]
	for i := start; i < len(buf); i++ {
		c := buf[i]
		if c == '"' {
			s.pos = i + 1
			return buf[start:i], nil
		}
		if c == '\\' || c < 0x20 {
			s.scratch = append(s.scratch[:0], buf[start:i]...)
			s.pos = i
			return s.readStringSlow()
		}
	}
	s.scratch = append(s.scratch[:0], buf[start:]...)
	s.pos = s.end
	return s.readStringSlow()
}

func (s *jsonStream) readStringSlow() ([]byte, error) {
	for {
		for s.pos < s.end {
			start := s.pos
			buf := s.buf[:s.end]
			i := start
			for i < len(buf) {
				c := buf[i]
				if c == '"' || c == '\\' || c < 0x20 {
					break
				}
				i++
			}
			if i > start {
				s.scratch = append(s.scratch, buf[start:i]...)
			}
			s.pos = i
			if i >= len(buf) {
				break
			}
			c := buf[i]
			if c == '"' {
				s.pos = i + 1
				return s.scratch, nil
			}
			if c < 0x20 {
				return nil, errors.New(errInvalidJSONSyntax)
			}
			// Handle escape sequence.
			s.pos = i + 1
			esc, err := s.nextByte()
			if err != nil {
				return nil, err
			}
			switch esc {
			case '"', '\\', '/':
				s.scratch = append(s.scratch, esc)
			case 'b':
				s.scratch = append(s.scratch, '\b')
			case 'f':
				s.scratch = append(s.scratch, '\f')
			case 'n':
				s.scratch = append(s.scratch, '\n')
			case 'r':
				s.scratch = append(s.scratch, '\r')
			case 't':
				s.scratch = append(s.scratch, '\t')
			case 'u':
				r1, err := parseHex4(s)
				if err != nil {
					return nil, err
				}
				if utf16.IsSurrogate(r1) {
					if r1 >= 0xD800 && r1 <= 0xDBFF {
						if b1, err := s.nextByte(); err == nil {
							if b1 == '\\' {
								b2, err := s.nextByte()
								if err != nil {
									return nil, err
								}
								if b2 == 'u' {
									r2, err := parseHex4(s)
									if err != nil {
										return nil, err
									}
									if r2 >= 0xDC00 && r2 <= 0xDFFF {
										s.scratch = utf8.AppendRune(s.scratch, utf16.DecodeRune(r1, r2))
										continue
									}
									s.scratch = utf8.AppendRune(s.scratch, utf8.RuneError)
									s.scratch = utf8.AppendRune(s.scratch, r2)
									continue
								}
								// Not \u: emit replacement for r1, then handle \b2.
								s.scratch = utf8.AppendRune(s.scratch, utf8.RuneError)
								if err := s.appendSimpleEscape(b2); err != nil {
									return nil, err
								}
								continue
							}
							// Put back non-backslash byte by handling it directly.
							s.scratch = utf8.AppendRune(s.scratch, utf8.RuneError)
							if b1 == '"' {
								return s.scratch, nil
							}
							if b1 < 0x20 {
								return nil, errors.New(errInvalidJSONSyntax)
							}
							s.scratch = append(s.scratch, b1)
							continue
						} else {
							return nil, err
						}
					}
					s.scratch = utf8.AppendRune(s.scratch, utf8.RuneError)
				} else {
					s.scratch = utf8.AppendRune(s.scratch, r1)
				}
			default:
				return nil, errors.New(errInvalidJSONSyntax)
			}
		}
		if !s.fill() {
			return nil, s.eofErr()
		}
	}
}

func (s *jsonStream) appendSimpleEscape(esc byte) error {
	switch esc {
	case '"', '\\', '/':
		s.scratch = append(s.scratch, esc)
	case 'b':
		s.scratch = append(s.scratch, '\b')
	case 'f':
		s.scratch = append(s.scratch, '\f')
	case 'n':
		s.scratch = append(s.scratch, '\n')
	case 'r':
		s.scratch = append(s.scratch, '\r')
	case 't':
		s.scratch = append(s.scratch, '\t')
	default:
		return errors.New(errInvalidJSONSyntax)
	}
	return nil
}

func (s *jsonStream) skipString() error {
	if err := s.expectByte('"'); err != nil {
		return err
	}
	for {
		buf := s.buf[:s.end]
		for i := s.pos; i < len(buf); i++ {
			c := buf[i]
			if c == '"' {
				s.pos = i + 1
				return nil
			}
			if c < 0x20 {
				return errors.New(errInvalidJSONSyntax)
			}
			if c == '\\' {
				s.pos = i + 1
				esc, err := s.nextByte()
				if err != nil {
					return err
				}
				switch esc {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				case 'u':
					if _, err := parseHex4(s); err != nil {
						return err
					}
				default:
					return errors.New(errInvalidJSONSyntax)
				}
				buf = s.buf[:s.end]
				i = s.pos - 1
			}
		}
		if !s.fill() {
			return s.eofErr()
		}
	}
}

func isNumberChar(c byte) bool {
	return (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E'
}

func (s *jsonStream) readNumberBytes() ([]byte, error) {
	c, err := s.peek()
	if err != nil {
		return nil, err
	}
	if (c < '0' || c > '9') && c != '-' {
		return nil, errors.New(errInvalidJSONSyntax)
	}
	start := s.pos
	buf := s.buf[:s.end]
	i := start
	for i < len(buf) && isNumberChar(buf[i]) {
		i++
	}
	if i < len(buf) {
		s.pos = i
		return buf[start:i], nil
	}
	s.scratch = append(s.scratch[:0], buf[start:]...)
	s.pos = s.end
	for {
		if !s.fill() {
			if s.err != nil {
				return nil, s.err
			}
			return s.scratch, nil
		}
		buf = s.buf[:s.end]
		i = 0
		for i < len(buf) && isNumberChar(buf[i]) {
			i++
		}
		s.scratch = append(s.scratch, buf[:i]...)
		s.pos = i
		if i < len(buf) {
			return s.scratch, nil
		}
	}
}

func (s *jsonStream) skipValue(depth int) error {
	if depth > maxStreamNesting {
		return errors.New(errInvalidJSONSyntax)
	}
	c, err := s.peek()
	if err != nil {
		return err
	}
	switch c {
	case '"':
		return s.skipString()
	case '{':
		s.pos++
		next, err := s.peek()
		if err != nil {
			return err
		}
		if next == '}' {
			s.pos++
			return nil
		}
		for {
			if err := s.skipString(); err != nil {
				return err
			}
			if err := s.expectByte(':'); err != nil {
				return err
			}
			if err := s.skipValue(depth + 1); err != nil {
				return err
			}
			sep, err := s.peek()
			if err != nil {
				return err
			}
			s.pos++
			if sep == '}' {
				return nil
			}
			if sep != ',' {
				return errors.New(errInvalidJSONSyntax)
			}
		}
	case '[':
		s.pos++
		next, err := s.peek()
		if err != nil {
			return err
		}
		if next == ']' {
			s.pos++
			return nil
		}
		for {
			if err := s.skipValue(depth + 1); err != nil {
				return err
			}
			sep, err := s.peek()
			if err != nil {
				return err
			}
			s.pos++
			if sep == ']' {
				return nil
			}
			if sep != ',' {
				return errors.New(errInvalidJSONSyntax)
			}
		}
	case 't':
		return s.expectLiteral("true")
	case 'f':
		return s.expectLiteral("false")
	case 'n':
		return s.expectLiteral("null")
	default:
		if c == '-' || (c >= '0' && c <= '9') {
			raw, err := s.readNumberBytes()
			if err != nil {
				return err
			}
			if _, err := strconv.ParseFloat(bytesAsString(raw), 64); err != nil {
				return errors.New(errInvalidJSONSyntax)
			}
			return nil
		}
		return errors.New(errInvalidJSONSyntax)
	}
}

// bytesAsString creates a non-escaping string view for immediate read-only use
// (such as strconv.ParseFloat). Callers must never retain the returned string.
func bytesAsString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

func (s *jsonStream) readOptionalString() (string, bool, error) {
	c, err := s.peek()
	if err != nil {
		return "", false, err
	}
	if c == 'n' {
		return "", false, s.expectLiteral("null")
	}
	b, err := s.readStringBytes()
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (s *jsonStream) decodeAdditional(row *itemRow) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c == 'n' {
		return s.expectLiteral("null")
	}
	if err := s.expectByte('{'); err != nil {
		return err
	}
	next, err := s.peek()
	if err != nil {
		return err
	}
	if next == '}' {
		s.pos++
		return nil
	}
	for {
		key, err := s.readStringBytes()
		if err != nil {
			return err
		}
		if err := s.expectByte(':'); err != nil {
			return err
		}
		switch string(key) {
		case "score":
			vc, err := s.peek()
			if err != nil {
				return err
			}
			if vc == 'n' {
				if err := s.expectLiteral("null"); err != nil {
					return err
				}
			} else {
				raw, err := s.readStringBytes()
				if err != nil {
					return err
				}
				row.scoreVal, _ = strconv.ParseFloat(bytesAsString(raw), 64)
				row.hasScore = true
			}
		case "distance":
			vc, err := s.peek()
			if err != nil {
				return err
			}
			if vc == 'n' {
				if err := s.expectLiteral("null"); err != nil {
					return err
				}
			} else {
				raw, err := s.readNumberBytes()
				if err != nil {
					return err
				}
				val, err := strconv.ParseFloat(bytesAsString(raw), 64)
				if err != nil {
					return errors.New(errInvalidJSONSyntax)
				}
				row.distanceVal = val
				row.hasDistance = true
			}
		default:
			if err := s.skipValue(0); err != nil {
				return err
			}
		}
		sep, err := s.peek()
		if err != nil {
			return err
		}
		s.pos++
		if sep == '}' {
			return nil
		}
		if sep != ',' {
			return errors.New(errInvalidJSONSyntax)
		}
	}
}

func (s *jsonStream) decodeItemRow(wantItem, wantText bool, lastGenID *string) (itemRow, error) {
	var row itemRow
	c, err := s.peek()
	if err != nil {
		return row, err
	}
	if c == 'n' {
		return row, s.expectLiteral("null")
	}
	if err := s.expectByte('{'); err != nil {
		return row, err
	}
	next, err := s.peek()
	if err != nil {
		return row, err
	}
	if next == '}' {
		s.pos++
		return row, nil
	}
	for {
		key, err := s.readStringBytes()
		if err != nil {
			return row, err
		}
		if err := s.expectByte(':'); err != nil {
			return row, err
		}
		switch string(key) {
		case "segmentId":
			val, _, err := s.readOptionalString()
			if err != nil {
				return row, err
			}
			row.SegmentID = val
		case "generationId":
			vc, err := s.peek()
			if err != nil {
				return row, err
			}
			if vc == 'n' {
				if err := s.expectLiteral("null"); err != nil {
					return row, err
				}
			} else {
				raw, err := s.readStringBytes()
				if err != nil {
					return row, err
				}
				if *lastGenID != "" && string(raw) == *lastGenID {
					row.GenerationID = *lastGenID
				} else {
					val := string(raw)
					*lastGenID = val
					row.GenerationID = val
				}
			}
		case "versionId":
			if wantItem {
				val, _, err := s.readOptionalString()
				if err != nil {
					return row, err
				}
				row.VersionID = val
			} else {
				if err := s.skipValue(0); err != nil {
					return row, err
				}
			}
		case passageText:
			if wantText {
				val, _, err := s.readOptionalString()
				if err != nil {
					return row, err
				}
				row.Text = val
			} else {
				if err := s.skipValue(0); err != nil {
					return row, err
				}
			}
		case "_additional":
			if err := s.decodeAdditional(&row); err != nil {
				return row, err
			}
		default:
			if err := s.skipValue(0); err != nil {
				return row, err
			}
		}
		sep, err := s.peek()
		if err != nil {
			return row, err
		}
		s.pos++
		if sep == '}' {
			return row, nil
		}
		if sep != ',' {
			return row, errors.New(errInvalidJSONSyntax)
		}
	}
}

func (s *jsonStream) decodeItemRowSlice(wantItem, wantText bool, limit int) ([]itemRow, error) {
	c, err := s.peek()
	if err != nil {
		return nil, err
	}
	if c == 'n' {
		return nil, s.expectLiteral("null")
	}
	if err := s.expectByte('['); err != nil {
		return nil, err
	}
	next, err := s.peek()
	if err != nil {
		return nil, err
	}
	if next == ']' {
		s.pos++
		return []itemRow{}, nil
	}
	initCap := min(max(limit, 8), 256)
	rows := make([]itemRow, 0, initCap)
	var lastGenID string
	for {
		row, err := s.decodeItemRow(wantItem, wantText, &lastGenID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
		sep, err := s.peek()
		if err != nil {
			return nil, err
		}
		s.pos++
		if sep == ']' {
			return rows, nil
		}
		if sep != ',' {
			return nil, errors.New(errInvalidJSONSyntax)
		}
	}
}

func decodeItemRows(r io.Reader, collection string, wantItem, wantText bool, limit int) ([]itemRow, error) {
	s := acquireJSONStream(r)
	defer releaseJSONStream(s)

	if err := s.expectByte('{'); err != nil {
		return nil, err
	}
	next, err := s.peek()
	if err != nil {
		return nil, err
	}
	var rows []itemRow
	var foundCollection bool
	var hasErrors bool
	if next == '}' {
		s.pos++
	} else {
		for {
			key, err := s.readStringBytes()
			if err != nil {
				return nil, err
			}
			if err := s.expectByte(':'); err != nil {
				return nil, err
			}
			switch string(key) {
			case "errors":
				vc, err := s.peek()
				if err != nil {
					return nil, err
				}
				if vc == 'n' {
					if err := s.expectLiteral("null"); err != nil {
						return nil, err
					}
				} else {
					if err := s.expectByte('['); err != nil {
						return nil, err
					}
					enext, err := s.peek()
					if err != nil {
						return nil, err
					}
					if enext == ']' {
						s.pos++
					} else {
						hasErrors = true
						for {
							if err := s.skipValue(0); err != nil {
								return nil, err
							}
							sep, err := s.peek()
							if err != nil {
								return nil, err
							}
							s.pos++
							if sep == ']' {
								break
							}
							if sep != ',' {
								return nil, errors.New(errInvalidJSONSyntax)
							}
						}
					}
				}
			case "data":
				vc, err := s.peek()
				if err != nil {
					return nil, err
				}
				if vc == 'n' {
					if err := s.expectLiteral("null"); err != nil {
						return nil, err
					}
				} else {
					if err := s.expectByte('{'); err != nil {
						return nil, err
					}
					dnext, err := s.peek()
					if err != nil {
						return nil, err
					}
					if dnext == '}' {
						s.pos++
					} else {
						for {
							dkey, err := s.readStringBytes()
							if err != nil {
								return nil, err
							}
							if err := s.expectByte(':'); err != nil {
								return nil, err
							}
							if string(dkey) == "Get" {
								gc, err := s.peek()
								if err != nil {
									return nil, err
								}
								if gc == 'n' {
									if err := s.expectLiteral("null"); err != nil {
										return nil, err
									}
								} else {
									if err := s.expectByte('{'); err != nil {
										return nil, err
									}
									gnext, err := s.peek()
									if err != nil {
										return nil, err
									}
									if gnext == '}' {
										s.pos++
									} else {
										for {
											gkey, err := s.readStringBytes()
											if err != nil {
												return nil, err
											}
											if err := s.expectByte(':'); err != nil {
												return nil, err
											}
											if string(gkey) == collection {
												foundCollection = true
												rows, err = s.decodeItemRowSlice(wantItem, wantText, limit)
												if err != nil {
													return nil, err
												}
											} else {
												if err := s.skipValue(0); err != nil {
													return nil, err
												}
											}
											gsep, err := s.peek()
											if err != nil {
												return nil, err
											}
											s.pos++
											if gsep == '}' {
												break
											}
											if gsep != ',' {
												return nil, errors.New(errInvalidJSONSyntax)
											}
										}
									}
								}
							} else {
								if err := s.skipValue(0); err != nil {
									return nil, err
								}
							}
							dsep, err := s.peek()
							if err != nil {
								return nil, err
							}
							s.pos++
							if dsep == '}' {
								break
							}
							if dsep != ',' {
								return nil, errors.New(errInvalidJSONSyntax)
							}
						}
					}
				}
			default:
				if err := s.skipValue(0); err != nil {
					return nil, err
				}
			}
			sep, err := s.peek()
			if err != nil {
				return nil, err
			}
			s.pos++
			if sep == '}' {
				break
			}
			if sep != ',' {
				return nil, errors.New(errInvalidJSONSyntax)
			}
		}
	}
	if hasErrors {
		return nil, errors.New("item projection query failed")
	}
	if !foundCollection {
		return nil, errors.New("item projection response missing")
	}
	return rows, nil
}

func identityConditions(q retrieval.Request) []filter {
	var out []filter
	for _, field := range []struct {
		name   string
		values []string
	}{{"recordId", q.RecordIDs}, {"versionId", q.VersionIDs}} {
		if len(field.values) > 0 {
			out = append(out, containsAny(field.name, field.values))
		}
	}
	return out
}

func (s *Store) itemWhere(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, kind string) (filter, error) {
	clauses := make([]filter, 0, len(routes))
	for _, r := range routes {
		if !className.MatchString(r.Generation.Collection) || r.Generation.ID == "" {
			return filter{}, errors.New("invalid projection route")
		}
		typed, missing, err := corpus.ResolveFilters(q.Metadata, r.Generation.Fields)
		if err != nil {
			return filter{}, err
		}
		if len(missing) > 0 {
			continue
		}
		if len(typed) > 0 && !r.Generation.MetadataProjected {
			return filter{}, retrieval.ErrMetadataFilterUnavailable
		}
		if err = s.ensureMetadata(ctx, r.Generation); err != nil {
			return filter{}, err
		}
		values := make([]filter, 0, 4+len(typed))
		values = append(values, routeVisibility(ctx, r.CorpusID))
		if tenantFrom(ctx) == "" {
			values = append(values, equal("corpusId", r.CorpusID), equal("generationId", r.Generation.ID))
		}
		if kind != "item" {
			values = append(values, projectionOwnerFilter(r.Generation, q.EvaluationPlugin))
		}
		for _, f := range typed {
			values = append(values, metadataConditionFor(r.Generation, f))
		}
		clauses = append(clauses, and(values...))
	}
	if len(clauses) == 0 {
		return filter{}, nil
	}
	kinds := equal(itemKind, kind)
	if kind == "vector" {
		// Include earlier enriched objects with the passage kind; nearVector
		// only scores objects carrying this target vector.
		kinds = or(kinds, equal(itemKind, "passage"), equal(itemKind, segmentKind))
	} else if kind == "passage" {
		kinds = or(kinds, equal(itemKind, segmentKind))
	}
	all := make([]filter, 0, 5)
	if tenantFrom(ctx) == "" {
		all = append(all, equal("organization", scope.Organization))
	}
	all = append(all, kinds, or(clauses...))
	if len(q.SourceNamespaces) > 0 {
		all = append(all, containsAny(sourceNamespaceProperty, q.SourceNamespaces))
	}
	all = append(all, identityConditions(q)...)
	return and(all...), nil
}

type candidateID struct {
	generationID string
	id           string
}

func candidateKey(c content.Candidate, grouped bool) candidateID {
	if grouped {
		return candidateID{generationID: c.GenerationID, id: c.VersionID}
	}
	return candidateID{generationID: c.GenerationID, id: c.SegmentID}
}

func rowCandidateKey(r itemRow, grouped bool) candidateID {
	if grouped {
		return candidateID{generationID: r.GenerationID, id: r.VersionID}
	}
	return candidateID{generationID: r.GenerationID, id: r.SegmentID}
}

func keepBest(pool map[candidateID]content.Candidate, c content.Candidate, grouped bool) {
	key := candidateKey(c, grouped)
	prev, ok := pool[key]
	if !ok || c.Score > prev.Score || (c.Score == prev.Score && c.SegmentID < prev.SegmentID) {
		pool[key] = c
	}
}

func ordered(pool map[candidateID]content.Candidate) []content.Candidate {
	out := make([]content.Candidate, 0, len(pool))
	for _, c := range pool {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].SegmentID != out[j].SegmentID {
			return out[i].SegmentID < out[j].SegmentID
		}
		if out[i].GenerationID != out[j].GenerationID {
			return out[i].GenerationID < out[j].GenerationID
		}
		return out[i].VersionID < out[j].VersionID
	})
	return out
}

// passageTerms compiles the distinct query terms once per analyzer. Passage
// selection scores each passage with minimum information density and within-item
// BM25 tie-breaking, taking the best score under any single analyzer.
type passageTerms struct {
	analyzer     keywords.Analyzer
	terms        map[string]struct{}
	orderedTerms []string
}

func itemPassageTerms(query string, analyzers []keywords.Analyzer) []passageTerms {
	compile := func(list []keywords.Analyzer) []passageTerms {
		var out []passageTerms
		for _, a := range list {
			terms := map[string]struct{}{}
			for word := range strings.FieldsSeq(a.Analyze(query)) {
				terms[word] = struct{}{}
			}
			if len(terms) > 0 {
				ordered := make([]string, 0, len(terms))
				for word := range terms {
					ordered = append(ordered, word)
				}
				sort.Strings(ordered)
				out = append(out, passageTerms{analyzer: a, terms: terms, orderedTerms: ordered})
			}
		}
		return out
	}
	if len(analyzers) == 0 {
		return compile([]keywords.Analyzer{keywords.Folded})
	}
	if out := compile(analyzers); len(out) > 0 {
		return out
	}
	// If configured analyzers drop every query token (for example, a query of
	// stopwords under a stemming analyzer), fall back to language-neutral folding
	// so passage selection still scores matches and information density.
	return compile([]keywords.Analyzer{keywords.Folded})
}

type passageScore struct {
	hasMatch    bool
	substantive bool
	coverage    int
	bm25        float64
	uniqueTerms int
	uniqueRunes int
}

func passageDensity(text string) (uniqueTerms, uniqueRunes int, substantive bool) {
	seen := map[string]struct{}{}
	for word := range strings.FieldsSeq(keywords.Folded.Analyze(text)) {
		if _, ok := seen[word]; !ok {
			seen[word] = struct{}{}
			uniqueRunes += utf8.RuneCountInString(word)
		}
	}
	uniqueTerms = len(seen)
	substantive = uniqueTerms >= minSubstantiveTerms && uniqueRunes >= minSubstantiveRunes
	return uniqueTerms, uniqueRunes, substantive
}

func betterPassage(a, b passageScore, segA, segB string) bool {
	if a.hasMatch != b.hasMatch {
		return a.hasMatch
	}
	if a.substantive != b.substantive {
		return a.substantive
	}
	if a.hasMatch {
		if a.coverage != b.coverage {
			return a.coverage > b.coverage
		}
		if a.bm25 != b.bm25 {
			return a.bm25 > b.bm25
		}
	} else {
		if a.uniqueTerms != b.uniqueTerms {
			return a.uniqueTerms > b.uniqueTerms
		}
		if a.uniqueRunes != b.uniqueRunes {
			return a.uniqueRunes > b.uniqueRunes
		}
	}
	return segA < segB
}

func pickBestPassage(group []itemRow, query []passageTerms) itemRow {
	n := len(group)
	scores := make([]passageScore, n)
	substantiveCount := 0
	for i, row := range group {
		uniqueTerms, uniqueRunes, substantive := passageDensity(row.Text)
		scores[i] = passageScore{
			substantive: substantive,
			uniqueTerms: uniqueTerms,
			uniqueRunes: uniqueRunes,
		}
		if substantive {
			substantiveCount++
		}
	}
	for _, q := range query {
		tfs := make([]map[string]int, n)
		dls := make([]float64, n)
		df := make(map[string]int, len(q.terms))
		totalDL := 0.0
		substantiveDL := 0.0
		for i, row := range group {
			count := 0
			var tf map[string]int
			for word := range strings.FieldsSeq(q.analyzer.Analyze(row.Text)) {
				count++
				if _, ok := q.terms[word]; ok {
					if tf == nil {
						tf = map[string]int{}
					}
					tf[word]++
				}
			}
			for word := range tf {
				df[word]++
			}
			dl := float64(count)
			if scores[i].substantive {
				dl = max(dl, passageBM25MinTerms)
				substantiveDL += dl
			}
			tfs[i] = tf
			dls[i] = dl
			totalDL += dl
		}
		avgdl := max(totalDL/float64(n), 1.0)
		if substantiveCount > 0 {
			avgdl = max(substantiveDL/float64(substantiveCount), passageBM25MinTerms)
		}
		for i := range group {
			cov := len(tfs[i])
			if cov == 0 || cov < scores[i].coverage {
				continue
			}
			norm := 1 - passageBM25B + passageBM25B*(dls[i]/avgdl)
			bm25 := 0.0
			for _, term := range q.orderedTerms {
				freq := tfs[i][term]
				if freq == 0 {
					continue
				}
				idf := math.Log(1 + (float64(n)-float64(df[term])+0.5)/(float64(df[term])+0.5))
				tfNorm := (float64(freq) * (passageBM25K1 + 1)) / (float64(freq) + passageBM25K1*norm)
				bm25 += idf * tfNorm
			}
			if cov > scores[i].coverage || (cov == scores[i].coverage && bm25 > scores[i].bm25) {
				scores[i].hasMatch = true
				scores[i].coverage = cov
				scores[i].bm25 = bm25
			}
		}
	}
	bestIdx := 0
	for i := 1; i < n; i++ {
		if betterPassage(scores[i], scores[bestIdx], group[i].SegmentID, group[bestIdx].SegmentID) {
			bestIdx = i
		}
	}
	return group[bestIdx]
}

var passageOrder = searchOrder{field: "segmentId"}

func passageSelectionLimit(q retrieval.Request, limit int) int {
	if q.Limit > 0 {
		// Keep modest headroom above q.Limit (including at retrieval.MaxLimit)
		// for candidates replacing entries filtered during hydration.
		headroom := min(q.Limit, maxPassageSelectionHeadroom)
		return min(limit, min(q.Limit, maxPassageSelectionItems)+headroom)
	}
	return min(limit, maxPassageSelectionItems)
}

func topPassageScanLimit(itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	ceiling := max(maxTopKPassageScan, min(maxItemPassages, itemCount*passagesPerTailItem))
	return min(ceiling, max(minTopKPassageScan, itemCount*passagesPerTopItem))
}

func tailPassageScanLimit(itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	ceiling := max(maxTailPassageScan, min(maxItemPassages, itemCount*2))
	return min(ceiling, max(itemCount, itemCount*passagesPerTailItem))
}

func versionFilter(items []content.Candidate) filter {
	seen := make(map[string]struct{}, len(items))
	ids := make([]string, 0, len(items))
	for _, c := range items {
		if _, ok := seen[c.VersionID]; ok {
			continue
		}
		seen[c.VersionID] = struct{}{}
		ids = append(ids, c.VersionID)
	}
	if len(ids) == 0 {
		return filter{}
	}
	return containsAny("versionId", ids)
}

func groupPassageRows(rows []itemRow) (map[candidateID]itemRow, map[candidateID][]itemRow) {
	best := make(map[candidateID]itemRow, len(rows))
	ambiguous := map[candidateID][]itemRow{}
	addPassageRows(best, ambiguous, rows)
	return best, ambiguous
}

func addPassageRows(best map[candidateID]itemRow, ambiguous map[candidateID][]itemRow, rows []itemRow) {
	seenSegments := make(map[candidateID]struct{}, len(rows)+len(best))
	for _, row := range best {
		seenSegments[rowCandidateKey(row, false)] = struct{}{}
	}
	for _, group := range ambiguous {
		for _, row := range group {
			seenSegments[rowCandidateKey(row, false)] = struct{}{}
		}
	}
	for _, row := range rows {
		if row.SegmentID == "" {
			continue
		}
		segment := rowCandidateKey(row, false)
		if _, dup := seenSegments[segment]; dup {
			continue
		}
		seenSegments[segment] = struct{}{}
		key := rowCandidateKey(row, true)
		first, seen := best[key]
		if !seen {
			best[key] = row
		} else {
			if len(ambiguous[key]) == 0 {
				ambiguous[key] = append(ambiguous[key], first)
			}
			ambiguous[key] = append(ambiguous[key], row)
		}
	}
}

func missingCandidates(items []content.Candidate, present map[candidateID]itemRow) []content.Candidate {
	var missing []content.Candidate
	for _, c := range items {
		if _, ok := present[candidateKey(c, true)]; !ok {
			missing = append(missing, c)
		}
	}
	return missing
}

// recoverMissingPassages guarantees that a high-passage item filling a shared
// segmentId-ordered scan window cannot starve other ranked versions.
func (s *Store) recoverMissingPassages(ctx context.Context, collection string, passageWhere filter, missing []content.Candidate, perVersionLimit int) ([]itemRow, error) {
	seen := make(map[string]struct{}, len(missing))
	versions := make([]string, 0, len(missing))
	for _, c := range missing {
		if c.VersionID == "" {
			continue
		}
		if _, ok := seen[c.VersionID]; ok {
			continue
		}
		seen[c.VersionID] = struct{}{}
		versions = append(versions, c.VersionID)
	}
	if len(versions) == 0 {
		return nil, nil
	}
	results := make([][]itemRow, len(versions))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(missingPassageConcurrency)
	for i, ver := range versions {
		goRecover(gctx, group, "recover_missing_passages", func() error {
			rows, err := s.searchRows(gctx, collection, searchBranch{}, and(passageWhere, equal("versionId", ver)), passageOrder, perVersionLimit, true, false)
			results[i] = rows
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var out []itemRow
	for _, rows := range results {
		out = append(out, rows...)
	}
	return out, nil
}

// Choose a representative only where text affects the answer. Ungrouped
// candidates all keep their segment IDs. With relative-score fusion a dense
// match supplies the representative and the sparse score is independent of it.
func (s *Store) resolveAmbiguousPassages(ctx context.Context, collection string, where filter, best map[candidateID]itemRow, ambiguous map[candidateID][]itemRow, query []passageTerms) (bool, error) {
	if len(ambiguous) == 0 {
		return false, nil
	}
	// Pin the text read to the canonical rows in the metadata window.
	// The same segment ID can appear in more than one generation.
	var rows []itemRow
	for _, group := range ambiguous {
		rows = append(rows, group...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].GenerationID == rows[j].GenerationID {
			return rows[i].SegmentID < rows[j].SegmentID
		}
		return rows[i].GenerationID < rows[j].GenerationID
	})
	var idFilter filter
	if tenantFrom(ctx) != "" {
		segIDs := make([]string, 0, len(rows))
		seen := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if _, ok := seen[row.SegmentID]; !ok {
				seen[row.SegmentID] = struct{}{}
				segIDs = append(segIDs, row.SegmentID)
			}
		}
		idFilter = containsAny("segmentId", segIDs)
	} else {
		var clauses []filter
		for i := 0; i < len(rows); {
			genID := rows[i].GenerationID
			var segIDs []string
			for i < len(rows) && rows[i].GenerationID == genID {
				if len(segIDs) == 0 || segIDs[len(segIDs)-1] != rows[i].SegmentID {
					segIDs = append(segIDs, rows[i].SegmentID)
				}
				i++
			}
			clauses = append(clauses, and(equal("generationId", genID), containsAny("segmentId", segIDs)))
		}
		if len(clauses) == 1 {
			idFilter = clauses[0]
		} else {
			idFilter = or(clauses...)
		}
	}
	// Reserve room for the seed and replacement of a segment during a swap.
	textRows, err := s.searchRows(ctx, collection, searchBranch{}, and(where, idFilter), passageOrder, 2*len(rows), true, true)
	if errors.Is(err, errResponseLimit) {
		// Retain the canonical first passage already captured per item in the
		// metadata window for byte-limit recovery.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	byItem := make(map[candidateID][]itemRow, len(ambiguous))
	seenSegments := make(map[candidateID]struct{}, len(textRows))
	for _, row := range textRows {
		if row.SegmentID == "" {
			continue
		}
		segment := rowCandidateKey(row, false)
		if _, dup := seenSegments[segment]; dup {
			continue
		}
		seenSegments[segment] = struct{}{}
		key := rowCandidateKey(row, true)
		if len(ambiguous[key]) == 0 {
			continue
		}
		byItem[key] = append(byItem[key], row)
	}
	for key, group := range byItem {
		best[key] = pickBestPassage(group, query)
	}
	return false, nil
}

type itemPartition struct {
	routes    []retrieval.Route
	fields    []corpus.Field
	analyzers []keywords.Analyzer
}

// itemSearch consolidates sparse copies and max passage scores BEFORE fusion.
// Alpha is applied once to one sparse/dense score per item, irrespective of
// the number of passages, analyzers, or Corpus configuration partitions.
func (s *Store) itemSearch(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	collection := routes[0].Generation.Collection
	grouped := q.GroupBy == "record"
	limit := q.K
	if limit == 0 {
		limit = retrieval.CandidateLimit
	}
	var orderedPartitions []*itemPartition
	if len(routes) == 1 {
		r := routes[0]
		if err := s.ensureItemSchema(ctx, r.Generation); err != nil {
			return nil, err
		}
		fields := content.ItemFields(r.Generation.Fields)
		orderedPartitions = []*itemPartition{{
			routes:    routes,
			fields:    fields,
			analyzers: fieldAnalyzers(fields),
		}}
	} else {
		partitions := map[string]*itemPartition{}
		var partitionKeys []string
		for _, r := range routes {
			if r.Generation.Collection != collection {
				return nil, errors.New("invalid projection route")
			}
			if err := s.ensureItemSchema(ctx, r.Generation); err != nil {
				return nil, err
			}
			fields := content.ItemFields(r.Generation.Fields)
			b, _ := json.Marshal(fields)
			key := string(b)
			if partitions[key] == nil {
				partitions[key] = &itemPartition{fields: fields, analyzers: fieldAnalyzers(fields)}
				partitionKeys = append(partitionKeys, key)
			}
			partitions[key].routes = append(partitions[key].routes, r)
		}
		sort.Strings(partitionKeys)
		orderedPartitions = make([]*itemPartition, len(partitionKeys))
		for i, key := range partitionKeys {
			orderedPartitions[i] = partitions[key]
		}
	}
	alpha, fusion := 0.5, retrieval.FusionRelativeScore
	if q.Hybrid != nil {
		alpha, fusion = q.Hybrid.Alpha, q.Hybrid.Fusion
	}
	var sparse, dense map[candidateID]content.Candidate
	legs, legCtx := errgroup.WithContext(ctx)
	denseDone := make(chan struct{})
	if q.Mode != "lexical" {
		goRecover(legCtx, legs, "item_dense", func() error {
			defer close(denseDone)
			var err error
			dense, err = s.itemDense(legCtx, collection, routes, scope, q, limit, grouped)
			return err
		})
	}
	if q.Mode != "semantic" {
		goRecover(legCtx, legs, "item_sparse", func() error {
			sparse = make(map[candidateID]content.Candidate, limit)
			var densePassages func() map[candidateID]content.Candidate
			if q.Mode != "lexical" && alpha > 0 && fusion != retrieval.FusionRanked {
				densePassages = func() map[candidateID]content.Candidate {
					select {
					case <-denseDone:
						return dense
					case <-legCtx.Done():
						return nil
					}
				}
			}
			for _, part := range orderedPartitions {
				if err := s.itemSparse(legCtx, collection, part, scope, q, limit, grouped, sparse, densePassages); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := legs.Wait(); err != nil {
		return nil, err
	}
	if q.Mode == "lexical" {
		return ordered(sparse), nil
	}
	if q.Mode == "semantic" {
		return ordered(dense), nil
	}
	return fuseItems(sparse, dense, alpha, fusion, grouped), nil
}

func (s *Store) itemSparse(ctx context.Context, collection string, p *itemPartition, scope corpus.Scope, q retrieval.Request, limit int, grouped bool, sparse map[candidateID]content.Candidate, densePassages func() map[candidateID]content.Candidate) error {
	where, err := s.itemWhere(ctx, p.routes, scope, q, "item")
	if err != nil || where.operator == 0 {
		return err
	}
	requests := itemBM25(p, q.Query)
	query := itemPassageTerms(q.Query, p.analyzers)
	items := make(map[candidateID]content.Candidate, limit)
	// An ownerless item may not yet have passages from the selected
	// ingestion owner. Allow one refill at twice the candidate depth.
	for round, depth := 0, limit; round < 2; round, depth = round+1, min(maxItemPassages, depth*2) {
		results := make([][]itemRow, len(requests))
		copies, copyCtx := errgroup.WithContext(ctx)
		for i, branch := range requests {
			goRecover(copyCtx, copies, "item_bm25", func() error {
				rows, err := s.searchRows(copyCtx, collection, branch, where, searchOrder{}, depth, true, false)
				results[i] = rows
				return err
			})
		}
		if err := copies.Wait(); err != nil {
			return err
		}
		more := false
		for _, rows := range results {
			more = more || len(rows) == depth
			for _, row := range rows {
				keepBest(items, row.candidate(false), true)
			}
		}
		if len(items) == 0 {
			break
		}
		passageWhere, err := s.itemWhere(ctx, p.routes, scope, q, "passage")
		if err != nil {
			return err
		}
		ranked := ordered(items)
		if len(ranked) > depth {
			ranked = ranked[:depth]
		}
		var rows []itemRow
		var exhausted bool
		if !grouped {
			scanLimit := min(maxItemPassages, max(limit, topPassageScanLimit(len(ranked))))
			rows, err = s.searchRows(ctx, collection, searchBranch{}, and(passageWhere, versionFilter(ranked)), passageOrder, scanLimit, true, false)
			if err != nil {
				return err
			}
			exhausted = len(rows) == scanLimit
			if exhausted {
				present, _ := groupPassageRows(rows)
				bounded := ranked[:min(len(ranked), passageSelectionLimit(q, limit))]
				if missing := missingCandidates(bounded, present); len(missing) > 0 {
					recovered, err := s.recoverMissingPassages(ctx, collection, passageWhere, missing, passagesPerTopItem)
					if err != nil {
						return err
					}
					rows = append(rows, recovered...)
				}
			}
		} else {
			rows, exhausted, err = s.selectSparseGroupedPassages(ctx, collection, passageWhere, ranked, query, passageSelectionLimit(q, limit), densePassages)
			if err != nil {
				return err
			}
		}
		chosen := make(map[candidateID]struct{}, len(rows))
		for _, row := range rows {
			if row.SegmentID == "" {
				continue
			}
			item, ok := items[rowCandidateKey(row, true)]
			if !ok {
				continue
			}
			c := row.candidate(false)
			c.Score = item.Score
			id := candidateKey(c, grouped)
			chosen[id] = struct{}{}
			// On equal scores keep the passage chosen before the refill.
			if prev, ok := sparse[id]; !ok || c.Score > prev.Score {
				sparse[id] = c
			}
		}
		if len(chosen) >= limit || !more || depth >= maxItemPassages || exhausted {
			break
		}
	}
	return nil
}

func (s *Store) selectSparseGroupedPassages(ctx context.Context, collection string, passageWhere filter, ranked []content.Candidate, query []passageTerms, selectK int, densePassages func() map[candidateID]content.Candidate) ([]itemRow, bool, error) {
	topCount := min(len(ranked), selectK)
	top := ranked[:topCount]
	tail := ranked[topCount:]

	topLimit := topPassageScanLimit(len(top))
	topRows, err := s.searchRows(ctx, collection, searchBranch{}, and(passageWhere, versionFilter(top)), passageOrder, topLimit, true, false)
	if err != nil {
		return nil, false, err
	}
	best, ambiguous := groupPassageRows(topRows)

	var denseMap map[candidateID]content.Candidate
	if densePassages != nil && (len(ambiguous) > 0 || len(tail) > 0 || len(best) < len(top)) {
		denseMap = densePassages()
		for key := range denseMap {
			delete(ambiguous, key)
		}
		for _, c := range top {
			key := candidateKey(c, true)
			if _, ok := best[key]; !ok {
				if d, ok := denseMap[key]; ok {
					best[key] = itemRow{SegmentID: d.SegmentID, GenerationID: d.GenerationID, VersionID: d.VersionID}
				}
			}
		}
	}

	// If a high-passage version filled the shared segmentId-ordered scan cap,
	// recover omitted top-ranked versions individually so none are starved.
	if len(topRows) == topLimit {
		if missingTop := missingCandidates(top, best); len(missingTop) > 0 {
			recovered, err := s.recoverMissingPassages(ctx, collection, passageWhere, missingTop, passagesPerTopItem)
			if err != nil {
				return nil, false, err
			}
			addPassageRows(best, ambiguous, recovered)
			for key := range denseMap {
				delete(ambiguous, key)
			}
		}
	}

	topCovered := 0
	for _, c := range top {
		if _, ok := best[candidateKey(c, true)]; ok {
			topCovered++
		}
	}

	var tailNeed []content.Candidate
	for _, c := range tail {
		key := candidateKey(c, true)
		if denseMap != nil {
			if d, ok := denseMap[key]; ok {
				best[key] = itemRow{SegmentID: d.SegmentID, GenerationID: d.GenerationID, VersionID: d.VersionID}
				continue
			}
		}
		tailNeed = append(tailNeed, c)
	}

	var exhausted bool
	if len(tailNeed) > 0 {
		tailLimit := tailPassageScanLimit(len(tailNeed))
		tailWhere := and(passageWhere, versionFilter(tailNeed))
		fetchTail := func(tailCtx context.Context, perMissing int) (map[candidateID]itemRow, map[candidateID][]itemRow, error) {
			tailRows, err := s.searchRows(tailCtx, collection, searchBranch{}, tailWhere, passageOrder, tailLimit, true, false)
			if err != nil {
				return nil, nil, err
			}
			tailBest, tailAmbiguous := groupPassageRows(tailRows)
			if len(tailRows) == tailLimit {
				if missingTail := missingCandidates(tailNeed, tailBest); len(missingTail) > 0 {
					recovered, err := s.recoverMissingPassages(tailCtx, collection, passageWhere, missingTail, perMissing)
					if err != nil {
						return nil, nil, err
					}
					addPassageRows(tailBest, tailAmbiguous, recovered)
				}
			}
			return tailBest, tailAmbiguous, nil
		}
		if topCovered >= selectK {
			var tailBest map[candidateID]itemRow
			var textExhausted bool
			group, gctx := errgroup.WithContext(ctx)
			goRecover(gctx, group, "resolve_ambiguous_passages", func() error {
				var err error
				textExhausted, err = s.resolveAmbiguousPassages(gctx, collection, passageWhere, best, ambiguous, query)
				return err
			})
			goRecover(gctx, group, "fetch_tail_passages", func() error {
				var err error
				tailBest, _, err = fetchTail(gctx, 1)
				return err
			})
			if err := group.Wait(); err != nil {
				return nil, false, err
			}
			exhausted = textExhausted
			for key, row := range tailBest {
				if _, ok := best[key]; !ok {
					best[key] = row
				}
			}
		} else {
			tailBest, tailAmbiguous, err := fetchTail(ctx, passagesPerTopItem)
			if err != nil {
				return nil, false, err
			}
			for _, c := range tail {
				key := candidateKey(c, true)
				if _, already := best[key]; already {
					if topCovered < selectK {
						topCovered++
					}
					continue
				}
				row, ok := tailBest[key]
				if !ok {
					continue
				}
				best[key] = row
				if topCovered < selectK {
					if g := tailAmbiguous[key]; len(g) > 0 {
						ambiguous[key] = g
					}
					topCovered++
				}
			}
			textExhausted, err := s.resolveAmbiguousPassages(ctx, collection, passageWhere, best, ambiguous, query)
			if err != nil {
				return nil, false, err
			}
			exhausted = textExhausted
		}
	} else {
		textExhausted, err := s.resolveAmbiguousPassages(ctx, collection, passageWhere, best, ambiguous, query)
		if err != nil {
			return nil, false, err
		}
		exhausted = textExhausted
	}

	selected := make([]itemRow, 0, len(best))
	for _, row := range best {
		selected = append(selected, row)
	}
	return selected, exhausted, nil
}

func itemBM25(p *itemPartition, query string) []searchBranch {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	analyzedQuery := make(map[string]string, len(p.analyzers))
	for _, a := range p.analyzers {
		if text := a.Analyze(query); strings.TrimSpace(text) != "" {
			analyzedQuery[a.Name] = text
		}
	}
	// Only include fields on the raw branch when they have no registered
	// analyzer or their analyzer dropped every query token (for example, an
	// all-stopword query), matching itemPassageTerms and preventing raw
	// stopword matches from masking analyzed stemming.
	var rawProps []string
	for _, f := range p.fields {
		if _, ok := keywords.Lookup(f.Analyzer); !ok || analyzedQuery[f.Analyzer] == "" {
			rawProps = append(rawProps, itemProperty(f)+"^"+strconv.Itoa(f.EffectiveBoost()))
		}
	}
	var branches []searchBranch
	if len(rawProps) > 0 {
		branches = append(branches, searchBranch{mode: "lexical", query: query, properties: rawProps})
	}
	for _, a := range p.analyzers {
		text := analyzedQuery[a.Name]
		if text == "" {
			continue
		}
		var props []string
		for _, f := range p.fields {
			if f.Analyzer != a.Name {
				continue
			}
			props = append(props, analyzedProperty(f, a)+"^"+strconv.Itoa(f.EffectiveBoost()))
		}
		if len(props) > 0 {
			branches = append(branches, searchBranch{mode: "lexical", query: text, properties: props})
		}
	}
	return branches
}

func (s *Store) itemDense(ctx context.Context, collection string, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request, limit int, grouped bool) (map[candidateID]content.Candidate, error) {
	where, err := s.itemWhere(ctx, routes, scope, q, "vector")
	if err != nil || where.operator == 0 {
		return nil, err
	}
	target := ""
	for _, r := range routes {
		space := q.Space
		if space == "" {
			space = r.Generation.SpaceID
		}
		name := s.VectorName(r.Generation, space)
		if target != "" && target != name {
			return nil, errors.New("routes store different vectors")
		}
		target = name
		if err := s.ensureVectors(ctx, r.Generation); err != nil {
			return nil, err
		}
	}
	branch := searchBranch{mode: "semantic", vector: q.Vector, target: target}
	dense := make(map[candidateID]content.Candidate, limit)
	maxDepth := limit
	if grouped {
		maxDepth = min(maxItemPassages, max(limit, min(maxDensePassageScan, max(minDensePassageScan, limit*4))))
	}
	for depth := limit; ; depth = min(maxDepth, depth*2) {
		rows, err := s.searchRows(ctx, collection, branch, where, searchOrder{}, depth, true, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.SegmentID != "" {
				keepBest(dense, r.candidate(true), grouped)
			}
		}
		if !grouped || len(dense) >= limit || len(rows) < depth || depth >= maxDepth {
			return dense, nil
		}
	}
}

func fuseItems(sparse, dense map[candidateID]content.Candidate, alpha float64, fusion string, grouped bool) []content.Candidate {
	out := make(map[candidateID]content.Candidate, max(len(sparse), len(dense)))
	for _, leg := range []struct {
		pool   map[candidateID]content.Candidate
		weight float64
		dense  bool
	}{{sparse, 1 - alpha, false}, {dense, alpha, true}} {
		rows := ordered(leg.pool)
		if len(rows) == 0 || leg.weight == 0 {
			continue
		}
		lo, hi := rows[len(rows)-1].Score, rows[0].Score
		for i, c := range rows {
			score := 1.0
			if fusion == retrieval.FusionRanked {
				score = 1 / float64(60+i+1)
			} else if hi != lo {
				score = (c.Score - lo) / (hi - lo)
			}
			key := candidateKey(c, grouped)
			prev, ok := out[key]
			if ok {
				if leg.dense {
					c.Score = prev.Score
				} else {
					c = prev
				}
			} else {
				c.Score = 0
			}
			c.Score += leg.weight * score
			out[key] = c
		}
	}
	return ordered(out)
}

// Search queries routes in partitions that one index request can rank
// together: by recipe (item keywords or passages) and, for a vector search,
// by the named vector that stores the space, which differs between
// generations built with different index settings.
func (s *Store) searchCollection(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 {
		return nil, errors.New("projection route missing")
	}
	type partition struct {
		item   bool
		routes []retrieval.Route
	}
	partitions := map[string]*partition{}
	var keys []string
	for _, r := range routes {
		// Partitions are merged, so every route must share one collection.
		if r.Generation.Collection != routes[0].Generation.Collection {
			return nil, errors.New("invalid projection route")
		}
		item := r.Generation.ItemKeywordsProjected && q.Field != retrieval.FieldLexical
		key := strconv.FormatBool(item)
		if q.Mode != "lexical" {
			space := q.Space
			if space == "" {
				space = r.Generation.SpaceID
			}
			key += "/" + s.VectorName(r.Generation, space)
		}
		if partitions[key] == nil {
			partitions[key] = &partition{item: item}
			keys = append(keys, key)
		}
		partitions[key].routes = append(partitions[key].routes, r)
	}
	sort.Strings(keys)
	var out []content.Candidate
	// Each partition's BM25 and hybrid scores are normalized over its own
	// candidates (item and passage BM25 also score different populations).
	// Reciprocal ranks put mixed partitions on a common scale without
	// reapplying hybrid alpha. Semantic scores are 1 minus the distance in
	// one space, comparable as they are.
	mixedRanks := len(keys) > 1 && q.Mode != "semantic"
	for _, key := range keys {
		p := partitions[key]
		var rows []content.Candidate
		var err error
		if p.item {
			rows, err = s.itemSearch(ctx, p.routes, scope, q)
		} else {
			if len(q.RecordIDs) > 0 {
				for _, r := range p.routes {
					if !r.Generation.ItemKeywordsProjected {
						return nil, retrieval.ErrMetadataFilterUnavailable
					}
				}
			}
			rows, err = s.searchLegacy(ctx, p.routes, scope, q)
		}
		if err != nil {
			return nil, err
		}
		if mixedRanks {
			rows = retrieval.NormalizeCandidateRanks(rows)
		}
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].SegmentID < out[j].SegmentID
	})
	if q.K > 0 && len(out) > q.K {
		out = out[:q.K]
	}
	return out, nil
}
