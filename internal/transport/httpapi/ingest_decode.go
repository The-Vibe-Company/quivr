package httpapi

import (
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

const maxJSONNestingDepth = 10000

var errMalformedJSON = errors.New("malformed_json")

// jsonScanner is a single-pass, zero-copy RFC 8259 JSON scanner over a UTF-8
// byte slice. Object keys are compared directly against raw slices on the
// unescaped fast path with zero heap allocations.
type jsonScanner struct {
	data  []byte
	pos   int
	depth int
}

func (s *jsonScanner) skipWS() {
	d := s.data
	i := s.pos
	for i < len(d) {
		switch d[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			s.pos = i
			return
		}
	}
	s.pos = i
}

// scanString scans a JSON string starting at s.pos (which must be '"').
// It returns raw content slice (excluding quotes), whether any escape sequence
// was present, and error.
func (s *jsonScanner) scanString() (raw []byte, escaped bool, err error) {
	d := s.data
	i := s.pos
	if i >= len(d) || d[i] != '"' {
		return nil, false, errMalformedJSON
	}
	i++
	start := i
	for i < len(d) {
		c := d[i]
		if c == '"' {
			s.pos = i + 1
			return d[start:i], escaped, nil
		}
		if c == '\\' {
			escaped = true
			i++
			if i >= len(d) {
				return nil, false, errMalformedJSON
			}
			switch d[i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i++
			case 'u':
				if i+4 >= len(d) {
					return nil, false, errMalformedJSON
				}
				for k := 1; k <= 4; k++ {
					if !isHexDigit(d[i+k]) {
						return nil, false, errMalformedJSON
					}
				}
				i += 5
			default:
				return nil, false, errMalformedJSON
			}
			continue
		}
		if c < 0x20 {
			return nil, false, errMalformedJSON
		}
		i++
	}
	return nil, false, errMalformedJSON
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// decodeString scans and decodes a JSON string into a Go string.
func (s *jsonScanner) decodeString() (string, error) {
	quoteStart := s.pos
	raw, escaped, err := s.scanString()
	if err != nil {
		return "", err
	}
	if !escaped {
		return string(raw), nil
	}
	var out string
	if err := json.Unmarshal(s.data[quoteStart:s.pos], &out); err != nil {
		return "", errMalformedJSON
	}
	return out, nil
}

// scanKey scans a JSON object key and returns a byte slice view on the
// unescaped fast path (zero allocs) or an unescaped byte slice on the slow path.
func (s *jsonScanner) scanKey() ([]byte, error) {
	quoteStart := s.pos
	raw, escaped, err := s.scanString()
	if err != nil {
		return nil, err
	}
	if !escaped {
		return raw, nil
	}
	var out string
	if err := json.Unmarshal(s.data[quoteStart:s.pos], &out); err != nil {
		return nil, errMalformedJSON
	}
	return []byte(out), nil
}

func (s *jsonScanner) expectColon() error {
	s.skipWS()
	if s.pos >= len(s.data) || s.data[s.pos] != ':' {
		return errMalformedJSON
	}
	s.pos++
	s.skipWS()
	return nil
}

// skipValue strictly validates and skips one complete RFC 8259 JSON value
// starting at s.pos (caller must call skipWS first).
func (s *jsonScanner) skipValue() error {
	if s.pos >= len(s.data) {
		return errMalformedJSON
	}
	switch s.data[s.pos] {
	case '{':
		s.depth++
		if s.depth > maxJSONNestingDepth {
			return errMalformedJSON
		}
		s.pos++
		s.skipWS()
		if s.pos < len(s.data) && s.data[s.pos] == '}' {
			s.pos++
			s.depth--
			return nil
		}
		for {
			if _, _, err := s.scanString(); err != nil {
				return err
			}
			if err := s.expectColon(); err != nil {
				return err
			}
			if err := s.skipValue(); err != nil {
				return err
			}
			s.skipWS()
			if s.pos >= len(s.data) {
				return errMalformedJSON
			}
			if s.data[s.pos] == ',' {
				s.pos++
				s.skipWS()
				continue
			}
			if s.data[s.pos] == '}' {
				s.pos++
				s.depth--
				return nil
			}
			return errMalformedJSON
		}
	case '[':
		s.depth++
		if s.depth > maxJSONNestingDepth {
			return errMalformedJSON
		}
		s.pos++
		s.skipWS()
		if s.pos < len(s.data) && s.data[s.pos] == ']' {
			s.pos++
			s.depth--
			return nil
		}
		for {
			if err := s.skipValue(); err != nil {
				return err
			}
			s.skipWS()
			if s.pos >= len(s.data) {
				return errMalformedJSON
			}
			if s.data[s.pos] == ',' {
				s.pos++
				s.skipWS()
				continue
			}
			if s.data[s.pos] == ']' {
				s.pos++
				s.depth--
				return nil
			}
			return errMalformedJSON
		}
	case '"':
		_, _, err := s.scanString()
		return err
	case 't':
		if s.pos+4 <= len(s.data) && string(s.data[s.pos:s.pos+4]) == "true" {
			s.pos += 4
			return nil
		}
		return errMalformedJSON
	case 'f':
		if s.pos+5 <= len(s.data) && string(s.data[s.pos:s.pos+5]) == "false" {
			s.pos += 5
			return nil
		}
		return errMalformedJSON
	case 'n':
		if s.pos+4 <= len(s.data) && string(s.data[s.pos:s.pos+4]) == "null" {
			s.pos += 4
			return nil
		}
		return errMalformedJSON
	default:
		return s.skipNumber()
	}
}

func (s *jsonScanner) skipNumber() error {
	d := s.data
	i := s.pos
	if i < len(d) && d[i] == '-' {
		i++
	}
	if i >= len(d) {
		return errMalformedJSON
	}
	if d[i] == '0' {
		i++
	} else if d[i] >= '1' && d[i] <= '9' {
		i++
		for i < len(d) && d[i] >= '0' && d[i] <= '9' {
			i++
		}
	} else {
		return errMalformedJSON
	}
	if i < len(d) && d[i] == '.' {
		i++
		if i >= len(d) || d[i] < '0' || d[i] > '9' {
			return errMalformedJSON
		}
		for i < len(d) && d[i] >= '0' && d[i] <= '9' {
			i++
		}
	}
	if i < len(d) && (d[i] == 'e' || d[i] == 'E') {
		i++
		if i < len(d) && (d[i] == '+' || d[i] == '-') {
			i++
		}
		if i >= len(d) || d[i] < '0' || d[i] > '9' {
			return errMalformedJSON
		}
		for i < len(d) && d[i] >= '0' && d[i] <= '9' {
			i++
		}
	}
	s.pos = i
	return nil
}

// parseBatchEnvelope validates the top-level BatchRequest envelope in a single
// zero-copy pass over payload, returning trimmed byte slices pointing directly
// into payload for each element of "items".
func parseBatchEnvelope(payload []byte) ([][]byte, error) {
	s := jsonScanner{data: payload}
	s.skipWS()
	if s.pos >= len(payload) {
		return nil, publicerr.MalformedJson
	}
	if payload[s.pos] != '{' {
		// Valid non-object JSON (e.g. a bare array) violates BatchRequest's
		// object schema; malformed syntax remains malformed_json.
		if err := s.skipValue(); err != nil {
			return nil, publicerr.MalformedJson
		}
		s.skipWS()
		if s.pos != len(payload) {
			return nil, publicerr.MalformedJson
		}
		return nil, publicerr.InvalidSchema
	}
	s.pos++ // consume '{'
	s.depth = 1
	s.skipWS()

	var (
		items         [][]byte
		seenItems     bool
		itemsIsArray  bool
		extraKey      bool
		extraKeysSeen map[string]struct{}
	)
	if s.pos < len(payload) && payload[s.pos] == '}' {
		s.pos++
	} else {
		for {
			keyBytes, err := s.scanKey()
			if err != nil {
				return nil, publicerr.MalformedJson
			}
			isItems := string(keyBytes) == "items"
			if isItems {
				if seenItems {
					return nil, publicerr.MalformedJson
				}
				seenItems = true
			} else {
				extraKey = true
				if extraKeysSeen == nil {
					extraKeysSeen = make(map[string]struct{}, 4)
				}
				k := string(keyBytes)
				if _, dup := extraKeysSeen[k]; dup {
					return nil, publicerr.MalformedJson
				}
				extraKeysSeen[k] = struct{}{}
			}
			if err := s.expectColon(); err != nil {
				return nil, publicerr.MalformedJson
			}
			if isItems && s.pos < len(payload) && payload[s.pos] == '[' {
				itemsIsArray = true
				s.depth++
				s.pos++ // consume '['
				s.skipWS()
				if s.pos < len(payload) && payload[s.pos] == ']' {
					s.pos++
					s.depth--
				} else {
					items = make([][]byte, 0, min(maxBatchEntries, 16))
					for {
						start := s.pos
						if err := s.skipValue(); err != nil {
							return nil, publicerr.MalformedJson
						}
						end := s.pos
						items = append(items, payload[start:end])
						s.skipWS()
						if s.pos >= len(payload) {
							return nil, publicerr.MalformedJson
						}
						if payload[s.pos] == ',' {
							s.pos++
							s.skipWS()
							continue
						}
						if payload[s.pos] == ']' {
							s.pos++
							s.depth--
							break
						}
						return nil, publicerr.MalformedJson
					}
				}
			} else {
				if err := s.skipValue(); err != nil {
					return nil, publicerr.MalformedJson
				}
			}
			s.skipWS()
			if s.pos >= len(payload) {
				return nil, publicerr.MalformedJson
			}
			if payload[s.pos] == ',' {
				s.pos++
				s.skipWS()
				continue
			}
			if payload[s.pos] == '}' {
				s.pos++
				break
			}
			return nil, publicerr.MalformedJson
		}
	}
	s.skipWS()
	if s.pos != len(payload) {
		return nil, publicerr.MalformedJson
	}
	if !seenItems || !itemsIsArray {
		return nil, publicerr.InvalidSchema
	}
	if len(items) > maxBatchEntries {
		return nil, publicerr.BatchTooLarge
	}
	if extraKey || len(items) == 0 {
		return nil, publicerr.InvalidSchema
	}
	return items, nil
}

// decodeIngestCommand parses and validates one IngestCommand directly into
// content.Command in a single pass, enforcing every constraint declared in
// contracts/http/v0/openapi.yaml#IngestCommand and
// contracts/shared/v0/manifest.schema.json.
//
// When checkSyntaxOnError is true (single-record POST /v0/records), malformed
// JSON syntax takes precedence over schema violations (returning
// publicerr.MalformedJson vs publicerr.InvalidSchema). For batch entries whose
// envelope was already syntax-validated, checkSyntaxOnError is false.
func decodeIngestCommand(raw []byte, checkSyntaxOnError bool) (content.Command, error) {
	s := jsonScanner{data: raw}
	cmd, err := s.parseIngestCommand()
	if err != nil {
		if checkSyntaxOnError {
			if errors.Is(err, errMalformedJSON) || !json.Valid(raw) {
				return content.Command{}, publicerr.MalformedJson
			}
		}
		return content.Command{}, publicerr.InvalidSchema
	}
	s.skipWS()
	if s.pos != len(raw) {
		if checkSyntaxOnError {
			return content.Command{}, publicerr.MalformedJson
		}
		return content.Command{}, publicerr.InvalidSchema
	}
	return cmd, nil
}

func (s *jsonScanner) parseIngestCommand() (content.Command, error) {
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Command{}, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Command{}, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Command{}, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Command{}, publicerr.InvalidSchema
	}

	const (
		cmdKey uint8 = 1 << iota
		cmdSource
		cmdContent
	)
	var (
		c        content.Command
		required uint8
	)

	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Command{}, err
		}
		if err := s.expectColon(); err != nil {
			return content.Command{}, err
		}
		switch string(key) {
		case "idempotency_key":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Command{}, err
			}
			c.Key = v
			required |= cmdKey
		case "source":
			src, err := s.parseSourceIdentity()
			if err != nil {
				return content.Command{}, err
			}
			c.Source = src
			required |= cmdSource
		case "content":
			text, manifest, err := s.parseCommandContent(true)
			if err != nil {
				return content.Command{}, err
			}
			c.Content = text
			c.Manifest = manifest
			required |= cmdContent
		case "record_acl":
			acl, err := s.parseRecordACL()
			if err != nil {
				return content.Command{}, err
			}
			c.ACL = acl
		case "source_revision":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Command{}, err
			}
			c.Revision = v
		case "source_position":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Command{}, err
			}
			if !isDecimalDigits(v) {
				return content.Command{}, publicerr.InvalidSchema
			}
			c.Position = v
		case "extensions":
			ext, err := s.parseExtensions()
			if err != nil {
				return content.Command{}, err
			}
			c.Extensions = ext
		case "provenance":
			prov, err := s.parseProvenance()
			if err != nil {
				return content.Command{}, err
			}
			c.Provenance = prov
		default:
			return content.Command{}, publicerr.InvalidSchema
		}

		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Command{}, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Command{}, errMalformedJSON
	}

	if required != (cmdKey | cmdSource | cmdContent) {
		return content.Command{}, publicerr.InvalidSchema
	}
	return c, nil
}

func (s *jsonScanner) decodeNonEmptyString() (string, error) {
	if s.pos >= len(s.data) {
		return "", errMalformedJSON
	}
	if s.data[s.pos] != '"' {
		return "", publicerr.InvalidSchema
	}
	v, err := s.decodeString()
	if err != nil {
		return "", err
	}
	if len(v) == 0 {
		return "", publicerr.InvalidSchema
	}
	return v, nil
}

func (s *jsonScanner) parseSourceIdentity() (content.Source, error) {
	if s.pos >= len(s.data) {
		return content.Source{}, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Source{}, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Source{}, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Source{}, publicerr.InvalidSchema
	}
	const (
		srcCorpus uint8 = 1 << iota
		srcNamespace
		srcRecordKey
	)
	var (
		out      content.Source
		required uint8
	)
	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Source{}, err
		}
		if err := s.expectColon(); err != nil {
			return content.Source{}, err
		}
		switch string(key) {
		case "corpus_id":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Source{}, err
			}
			out.CorpusID = v
			required |= srcCorpus
		case "namespace":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Source{}, err
			}
			out.Namespace = v
			required |= srcNamespace
		case "record_key":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Source{}, err
			}
			out.RecordKey = v
			required |= srcRecordKey
		default:
			return content.Source{}, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Source{}, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Source{}, errMalformedJSON
	}
	if required != (srcCorpus | srcNamespace | srcRecordKey) {
		return content.Source{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (s *jsonScanner) parseCommandContent(allowManifest bool) (content.Text, *content.Manifest, error) {
	if s.pos >= len(s.data) {
		return content.Text{}, nil, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Text{}, nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Text{}, nil, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Text{}, nil, publicerr.InvalidSchema
	}
	const (
		fKind uint8 = 1 << iota
		fText
		fBlobID
		fMediaType
		fParts
		fRelations
	)
	var (
		seen      uint8
		kind      string
		text      string
		blobID    string
		mediaType string
		parts     []content.Part
		relations []content.Relation
	)
	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Text{}, nil, err
		}
		if err := s.expectColon(); err != nil {
			return content.Text{}, nil, err
		}
		switch string(key) {
		case "kind":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Text{}, nil, err
			}
			kind = v
			seen |= fKind
		case "text":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Text{}, nil, err
			}
			text = v
			seen |= fText
		case "blob_id":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Text{}, nil, err
			}
			blobID = v
			seen |= fBlobID
		case "media_type":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Text{}, nil, err
			}
			mediaType = v
			seen |= fMediaType
		case "parts":
			if !allowManifest {
				return content.Text{}, nil, publicerr.InvalidSchema
			}
			p, err := s.parseParts()
			if err != nil {
				return content.Text{}, nil, err
			}
			parts = p
			seen |= fParts
		case "relations":
			if !allowManifest {
				return content.Text{}, nil, publicerr.InvalidSchema
			}
			r, err := s.parseRelations()
			if err != nil {
				return content.Text{}, nil, err
			}
			relations = r
			seen |= fRelations
		default:
			return content.Text{}, nil, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Text{}, nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Text{}, nil, errMalformedJSON
	}

	switch kind {
	case "text":
		if seen != (fKind | fText) {
			return content.Text{}, nil, publicerr.InvalidSchema
		}
		return content.Text{Kind: "text", Text: text}, nil, nil
	case "blob":
		if seen != (fKind | fBlobID | fMediaType) {
			return content.Text{}, nil, publicerr.InvalidSchema
		}
		return content.Text{Kind: "blob", BlobID: blobID, MediaType: mediaType}, nil, nil
	case "manifest":
		if !allowManifest || seen&^(fKind|fParts|fRelations) != 0 || seen&(fKind|fParts) != (fKind|fParts) || len(parts) == 0 {
			return content.Text{}, nil, publicerr.InvalidSchema
		}
		if relations == nil {
			relations = []content.Relation{}
		}
		return content.Text{Kind: "manifest"}, &content.Manifest{Kind: "manifest", Parts: parts, Relations: relations}, nil
	default:
		return content.Text{}, nil, publicerr.InvalidSchema
	}
}

func (s *jsonScanner) parseParts() ([]content.Part, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] == ']' {
		s.pos++
		return nil, publicerr.InvalidSchema // minItems: 1
	}
	parts := make([]content.Part, 0, 4)
	for {
		p, err := s.parsePart()
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == ']' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return parts, nil
}

func (s *jsonScanner) parsePart() (content.Part, error) {
	if s.pos >= len(s.data) {
		return content.Part{}, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Part{}, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Part{}, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Part{}, publicerr.InvalidSchema
	}
	const (
		pKey uint8 = 1 << iota
		pRole
		pContent
	)
	var (
		out      content.Part
		required uint8
	)
	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Part{}, err
		}
		if err := s.expectColon(); err != nil {
			return content.Part{}, err
		}
		switch string(key) {
		case "key":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Part{}, err
			}
			out.Key = v
			required |= pKey
		case "parent_key":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Part{}, err
			}
			out.ParentKey = v
		case "role":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Part{}, err
			}
			out.Role = v
			required |= pRole
		case "content":
			text, _, err := s.parseCommandContent(false)
			if err != nil {
				return content.Part{}, err
			}
			out.Content = text
			required |= pContent
		case "extensions":
			ext, err := s.parseExtensions()
			if err != nil {
				return content.Part{}, err
			}
			out.Extensions = ext
		default:
			return content.Part{}, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Part{}, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Part{}, errMalformedJSON
	}
	if required != (pKey | pRole | pContent) {
		return content.Part{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (s *jsonScanner) parseRelations() ([]content.Relation, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] == ']' {
		s.pos++
		return []content.Relation{}, nil
	}
	relations := make([]content.Relation, 0, 4)
	for {
		r, err := s.parseRelation()
		if err != nil {
			return nil, err
		}
		relations = append(relations, r)
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == ']' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return relations, nil
}

func (s *jsonScanner) parseRelation() (content.Relation, error) {
	if s.pos >= len(s.data) {
		return content.Relation{}, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Relation{}, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Relation{}, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Relation{}, publicerr.InvalidSchema
	}
	const (
		rType uint8 = 1 << iota
		rTarget
	)
	var (
		out      content.Relation
		required uint8
	)
	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Relation{}, err
		}
		if err := s.expectColon(); err != nil {
			return content.Relation{}, err
		}
		switch string(key) {
		case "type":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Relation{}, err
			}
			out.Type = v
			required |= rType
		case "target":
			src, err := s.parseSourceIdentity()
			if err != nil {
				return content.Relation{}, err
			}
			out.Target = src
			required |= rTarget
		case "source_target_revision":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Relation{}, err
			}
			out.SourceTargetRevision = v
		default:
			return content.Relation{}, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Relation{}, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Relation{}, errMalformedJSON
	}
	if required != (rType | rTarget) {
		return content.Relation{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (s *jsonScanner) parseExtensions() (content.Extensions, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	out := make(content.Extensions)
	if s.data[s.pos] == '}' {
		s.pos++
		return out, nil
	}
	for {
		ns, err := s.decodeString()
		if err != nil {
			return nil, err
		}
		if err := s.expectColon(); err != nil {
			return nil, err
		}
		ext, err := s.parseExtensionValue()
		if err != nil {
			return nil, err
		}
		out[ns] = ext
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return out, nil
}

func (s *jsonScanner) parseExtensionValue() (content.Extension, error) {
	if s.pos >= len(s.data) {
		return content.Extension{}, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return content.Extension{}, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return content.Extension{}, errMalformedJSON
	}
	if s.data[s.pos] == '}' {
		s.pos++
		return content.Extension{}, publicerr.InvalidSchema
	}
	const (
		eVersion uint8 = 1 << iota
		eData
	)
	var (
		out      content.Extension
		required uint8
	)
	for {
		key, err := s.scanKey()
		if err != nil {
			return content.Extension{}, err
		}
		if err := s.expectColon(); err != nil {
			return content.Extension{}, err
		}
		switch string(key) {
		case "schema_version":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return content.Extension{}, err
			}
			out.SchemaVersion = v
			required |= eVersion
		case "data":
			if s.pos >= len(s.data) {
				return content.Extension{}, errMalformedJSON
			}
			if s.data[s.pos] != '{' {
				return content.Extension{}, publicerr.InvalidSchema
			}
			start := s.pos
			if err := s.skipValue(); err != nil {
				return content.Extension{}, err
			}
			var data map[string]any
			if err := json.Unmarshal(s.data[start:s.pos], &data); err != nil {
				return content.Extension{}, errMalformedJSON
			}
			out.Data = data
			required |= eData
		default:
			return content.Extension{}, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return content.Extension{}, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return content.Extension{}, errMalformedJSON
	}
	if required != (eVersion | eData) {
		return content.Extension{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (s *jsonScanner) parseProvenance() (map[string]any, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '{' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	out := map[string]any{}
	if s.data[s.pos] == '}' {
		s.pos++
		return out, nil
	}
	for {
		key, err := s.scanKey()
		if err != nil {
			return nil, err
		}
		if err := s.expectColon(); err != nil {
			return nil, err
		}
		switch string(key) {
		case "producer":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return nil, err
			}
			out["producer"] = v
		case "producer_version":
			v, err := s.decodeNonEmptyString()
			if err != nil {
				return nil, err
			}
			out["producer_version"] = v
		case "source_blob_ids":
			ids, err := s.parseSourceBlobIDs()
			if err != nil {
				return nil, err
			}
			out["source_blob_ids"] = ids
		case "normalization":
			// Engine-owned: rejected on input (same InvalidSchema outcome as
			// commandFromTransport rejecting in.Provenance.Normalization != nil).
			return nil, publicerr.InvalidSchema
		default:
			return nil, publicerr.InvalidSchema
		}
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == '}' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return out, nil
}

func (s *jsonScanner) parseSourceBlobIDs() ([]any, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	ids := []any{}
	if s.data[s.pos] == ']' {
		s.pos++
		return ids, nil
	}
	for {
		v, err := s.decodeNonEmptyString()
		if err != nil {
			return nil, err
		}
		ids = append(ids, v)
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == ']' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return ids, nil
}

func (s *jsonScanner) parseRecordACL() ([]string, error) {
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	s.pos++
	s.skipWS()
	if s.pos >= len(s.data) {
		return nil, errMalformedJSON
	}
	if s.data[s.pos] == ']' {
		s.pos++
		return nil, publicerr.InvalidSchema // minItems: 1
	}
	const maxRecordACLItems = 64
	acl := make([]string, 0, 4)
	for {
		v, err := s.decodeNonEmptyString()
		if err != nil {
			return nil, err
		}
		if !isVisibilityPrincipal(v) {
			return nil, publicerr.InvalidSchema
		}
		if len(acl) >= maxRecordACLItems {
			return nil, publicerr.InvalidSchema
		}
		for _, prev := range acl {
			if prev == v {
				return nil, publicerr.InvalidSchema // uniqueItems: true
			}
		}
		acl = append(acl, v)
		s.skipWS()
		if s.pos >= len(s.data) {
			return nil, errMalformedJSON
		}
		if s.data[s.pos] == ',' {
			s.pos++
			s.skipWS()
			continue
		}
		if s.data[s.pos] == ']' {
			s.pos++
			break
		}
		return nil, errMalformedJSON
	}
	return acl, nil
}

func isDecimalDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isVisibilityPrincipal matches ^[A-Za-z0-9_.-]{1,32}:[A-Za-z0-9_.-]{1,128}$.
func isVisibilityPrincipal(s string) bool {
	colon := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' {
			if colon >= 0 {
				return false
			}
			colon = i
			continue
		}
		if !isPrincipalChar(c) {
			return false
		}
	}
	if colon < 1 || colon > 32 {
		return false
	}
	rightLen := len(s) - colon - 1
	return rightLen >= 1 && rightLen <= 128
}

func isPrincipalChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-'
}
