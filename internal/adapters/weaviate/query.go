package weaviate

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
	"unsafe"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
)

type filter struct {
	operator pb.Filters_Operator
	field    string
	jsonKey  string
	value    any
	children []filter
}

func equal(field, value string) filter {
	return filter{operator: pb.Filters_OPERATOR_EQUAL, field: field, jsonKey: "valueText", value: value}
}

func notEqual(field, value string) filter {
	return filter{operator: pb.Filters_OPERATOR_NOT_EQUAL, field: field, jsonKey: "valueText", value: value}
}

func and(operands ...filter) filter {
	children := make([]filter, 0, len(operands))
	for _, operand := range operands {
		if operand.operator != 0 {
			children = append(children, operand)
		}
	}
	if len(children) == 0 {
		return filter{}
	}
	return filter{operator: pb.Filters_OPERATOR_AND, children: children}
}

func or(operands ...filter) filter {
	for _, operand := range operands {
		if operand.operator == 0 {
			return filter{}
		}
	}
	return filter{operator: pb.Filters_OPERATOR_OR, children: operands}
}

func containsAny(field string, values []string) filter {
	return filter{operator: pb.Filters_OPERATOR_CONTAINS_ANY, field: field, jsonKey: "valueText", value: values}
}

func graphOperator(op pb.Filters_Operator) string {
	switch op {
	case pb.Filters_OPERATOR_EQUAL:
		return "Equal"
	case pb.Filters_OPERATOR_NOT_EQUAL:
		return "NotEqual"
	case pb.Filters_OPERATOR_AND:
		return "And"
	case pb.Filters_OPERATOR_OR:
		return "Or"
	case pb.Filters_OPERATOR_CONTAINS_ANY:
		return "ContainsAny"
	case pb.Filters_OPERATOR_GREATER_THAN_EQUAL:
		return "GreaterThanEqual"
	case pb.Filters_OPERATOR_LESS_THAN_EQUAL:
		return "LessThanEqual"
	default:
		return ""
	}
}

func (f filter) encodedLen() int {
	op := graphOperator(f.operator)
	if f.field == "" {
		n := len("{operator:,operands:[]}") + len(op)
		for i, child := range f.children {
			if i > 0 {
				n++
			}
			n += child.encodedLen()
		}
		return n
	}
	n := len("{path:[],operator:,:}") + len(f.field) + 2 + len(op) + len(f.jsonKey)
	switch v := f.value.(type) {
	case string:
		n += len(v) + 2
	case []string:
		if v == nil {
			n += 4
		} else {
			n += 2
			for i, s := range v {
				if i > 0 {
					n++
				}
				n += len(s) + 2
			}
		}
	case float64:
		n += 24
	case bool:
		n += 5
	default:
		n += 32
	}
	return n
}

func (f filter) appendGraphQL(dst []byte) []byte {
	op := graphOperator(f.operator)
	if f.field == "" {
		dst = append(dst, "{operator:"...)
		dst = append(dst, op...)
		dst = append(dst, ",operands:["...)
		for i, child := range f.children {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = child.appendGraphQL(dst)
		}
		return append(dst, "]}"...)
	}
	dst = append(dst, "{path:["...)
	dst = appendJSONString(dst, f.field)
	dst = append(dst, "],operator:"...)
	dst = append(dst, op...)
	dst = append(dst, ',')
	dst = append(dst, f.jsonKey...)
	dst = append(dst, ':')
	dst = appendJSONValue(dst, f.value)
	return append(dst, '}')
}

func ownedBytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func (f filter) String() string {
	buf := make([]byte, 0, f.encodedLen())
	buf = f.appendGraphQL(buf)
	return ownedBytesToString(buf)
}

func generationFilter(ctx context.Context, org, generationID string) filter {
	if tenantFrom(ctx) != "" {
		return filter{}
	}
	return and(equal("organization", org), equal("generationId", generationID))
}

func (f filter) graphql() string {
	if f.operator == 0 {
		return ""
	}
	buf := make([]byte, 0, len("where:")+f.encodedLen()+1)
	buf = append(buf, "where:"...)
	buf = f.appendGraphQL(buf)
	buf = append(buf, ',')
	return ownedBytesToString(buf)
}

func (f filter) proto() *pb.Filters {
	if f.operator == 0 {
		return nil
	}
	out := &pb.Filters{Operator: f.operator}
	if f.field == "" {
		if len(f.children) > 0 {
			out.Filters = make([]*pb.Filters, 0, len(f.children))
			for _, child := range f.children {
				out.Filters = append(out.Filters, child.proto())
			}
		}
		return out
	}
	field := f.field
	if field == "id" {
		// gRPC filters address the internal UUID property directly.
		field = "_id"
	}
	out.Target = &pb.FilterTarget{Target: &pb.FilterTarget_Property{Property: field}}
	switch value := f.value.(type) {
	case string:
		out.TestValue = &pb.Filters_ValueText{ValueText: value}
	case []string:
		out.TestValue = &pb.Filters_ValueTextArray{ValueTextArray: &pb.TextArray{Values: value}}
	case float64:
		out.TestValue = &pb.Filters_ValueNumber{ValueNumber: value}
	case bool:
		out.TestValue = &pb.Filters_ValueBoolean{ValueBoolean: value}
	}
	return out
}

type searchBranch struct {
	mode       string
	query      string
	properties []string
	vector     []float32
	target     string
	alpha      float64
	ranked     bool
}

func (b searchBranch) encodedLen() int {
	switch b.mode {
	case "lexical":
		n := len("bm25:{query:,properties:[]},") + len(b.query) + 2
		for _, p := range b.properties {
			n += len(p) + 3
		}
		return n
	case "semantic":
		return len("nearVector:{vector:[],targetVectors:[]},") + len(b.vector)*10 + len(b.target) + 2
	case "hybrid":
		n := len("hybrid:{query:,vector:[],alpha:,fusionType:relativeScoreFusion,properties:[],targetVectors:[]},") +
			len(b.query) + 2 + len(b.vector)*10 + 16 + len(b.target) + 2
		for _, p := range b.properties {
			n += len(p) + 3
		}
		return n
	default:
		return 0
	}
}

func (b searchBranch) appendGraphQL(dst []byte) []byte {
	switch b.mode {
	case "lexical":
		dst = append(dst, "bm25:{query:"...)
		dst = appendJSONString(dst, b.query)
		dst = append(dst, ",properties:"...)
		dst = appendJSONStrings(dst, b.properties)
		return append(dst, "},"...)
	case "semantic":
		dst = append(dst, "nearVector:{vector:"...)
		dst = appendJSONFloat32Slice(dst, b.vector)
		dst = append(dst, ",targetVectors:["...)
		dst = appendJSONString(dst, b.target)
		return append(dst, "]},"...)
	case "hybrid":
		fusion := "relativeScoreFusion"
		if b.ranked {
			fusion = "rankedFusion"
		}
		dst = append(dst, "hybrid:{query:"...)
		dst = appendJSONString(dst, b.query)
		dst = append(dst, ",vector:"...)
		dst = appendJSONFloat32Slice(dst, b.vector)
		dst = append(dst, ",alpha:"...)
		dst = strconv.AppendFloat(dst, b.alpha, 'f', -1, 64)
		dst = append(dst, ",fusionType:"...)
		dst = append(dst, fusion...)
		dst = append(dst, ",properties:"...)
		dst = appendJSONStrings(dst, b.properties)
		dst = append(dst, ",targetVectors:["...)
		dst = appendJSONString(dst, b.target)
		return append(dst, "]},"...)
	}
	return dst
}

func (b searchBranch) graphql() string {
	if b.mode != "lexical" && b.mode != "semantic" && b.mode != "hybrid" {
		return ""
	}
	buf := make([]byte, 0, b.encodedLen())
	buf = b.appendGraphQL(buf)
	return ownedBytesToString(buf)
}

type searchOrder struct {
	field string
	desc  bool
}

func (o searchOrder) encodedLen() int {
	if o.field == "" {
		return 0
	}
	return len("sort:[{path:[],order:desc}],") + len(o.field) + 2
}

func (o searchOrder) appendGraphQL(dst []byte) []byte {
	if o.field == "" {
		return dst
	}
	dst = append(dst, "sort:[{path:["...)
	dst = appendJSONString(dst, o.field)
	if o.desc {
		return append(dst, "],order:desc}],"...)
	}
	return append(dst, "],order:asc}],"...)
}

func (o searchOrder) graphql() string {
	if o.field == "" {
		return ""
	}
	buf := make([]byte, 0, o.encodedLen())
	buf = o.appendGraphQL(buf)
	return ownedBytesToString(buf)
}

func quote(value string) string {
	buf := make([]byte, 0, len(value)+2)
	buf = appendJSONString(buf, value)
	return ownedBytesToString(buf)
}

func encodeJSON(value any) string {
	buf := appendJSONValue(nil, value)
	return ownedBytesToString(buf)
}

const hexDigits = "0123456789abcdef"

var safeJSONChar = func() [256]bool {
	var t [256]bool
	for c := 0x20; c < utf8.RuneSelf; c++ {
		if c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
			t[c] = true
		}
	}
	return t
}()

// appendJSONString appends s as a JSON string literal byte-identical to Go
// 1.27's encoding/json.Marshal(s), including HTML escapes, control characters,
// line/paragraph separators, and UTF-8 U+FFFD replacement for invalid bytes.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if safeJSONChar[c] {
			i++
			continue
		}
		if c < utf8.RuneSelf {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			switch c {
			case '"':
				dst = append(dst, '\\', '"')
			case '\\':
				dst = append(dst, '\\', '\\')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			case '<':
				dst = append(dst, '\\', 'u', '0', '0', '3', 'c')
			case '>':
				dst = append(dst, '\\', 'u', '0', '0', '3', 'e')
			case '&':
				dst = append(dst, '\\', 'u', '0', '0', '2', '6')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0x0f])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			dst = append(dst, "\ufffd"...)
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			if r == '\u2028' {
				dst = append(dst, `\u2028`...)
			} else {
				dst = append(dst, `\u2029`...)
			}
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		dst = append(dst, s[start:]...)
	}
	return append(dst, '"')
}

func appendJSONStrings(dst []byte, values []string) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	dst = append(dst, '[')
	for i, s := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendJSONString(dst, s)
	}
	return append(dst, ']')
}

func appendJSONFloat(dst []byte, v float64, bitSize int) []byte {
	abs := math.Abs(v)
	fmtByte := byte('f')
	if abs != 0 {
		if bitSize == 32 {
			abs32 := float32(abs)
			if abs32 < 1e-6 || abs32 >= 1e21 {
				fmtByte = 'e'
			}
		} else if abs < 1e-6 || abs >= 1e21 {
			fmtByte = 'e'
		}
	}
	start := len(dst)
	dst = strconv.AppendFloat(dst, v, fmtByte, -1, bitSize)
	if fmtByte == 'e' {
		// Match encoding/json exponent formatting: strip leading zero in 2-digit exponents (e.g. e-07 -> e-7).
		n := len(dst)
		if n-start >= 4 && dst[n-4] == 'e' && (dst[n-3] == '-' || dst[n-3] == '+') && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

func appendJSONFloat32Slice(dst []byte, values []float32) []byte {
	if values == nil {
		return append(dst, "null"...)
	}
	dst = append(dst, '[')
	for i, v := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			dst = append(dst, "null"...)
			continue
		}
		dst = appendJSONFloat(dst, f, 32)
	}
	return append(dst, ']')
}

func appendJSONValue(dst []byte, value any) []byte {
	switch v := value.(type) {
	case string:
		return appendJSONString(dst, v)
	case []string:
		return appendJSONStrings(dst, v)
	case []float32:
		return appendJSONFloat32Slice(dst, v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			raw, _ := json.Marshal(v)
			return append(dst, raw...)
		}
		return appendJSONFloat(dst, v, 64)
	case bool:
		if v {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	default:
		raw, _ := json.Marshal(value)
		return append(dst, raw...)
	}
}
