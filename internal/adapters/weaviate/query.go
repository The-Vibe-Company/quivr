package weaviate

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

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

func (f filter) writeGraphQL(b *strings.Builder) {
	op := graphOperator(f.operator)
	if f.field == "" {
		b.WriteString("{operator:")
		b.WriteString(op)
		b.WriteString(",operands:[")
		for i, child := range f.children {
			if i > 0 {
				b.WriteByte(',')
			}
			child.writeGraphQL(b)
		}
		b.WriteString("]}")
		return
	}
	b.WriteString("{path:[")
	writeJSONString(b, f.field)
	b.WriteString("],operator:")
	b.WriteString(op)
	b.WriteByte(',')
	b.WriteString(f.jsonKey)
	b.WriteByte(':')
	writeJSONValue(b, f.value)
	b.WriteByte('}')
}

func (f filter) String() string {
	var b strings.Builder
	b.Grow(f.encodedLen())
	f.writeGraphQL(&b)
	return b.String()
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
	var b strings.Builder
	b.Grow(len("where:") + f.encodedLen() + 1)
	b.WriteString("where:")
	f.writeGraphQL(&b)
	b.WriteByte(',')
	return b.String()
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

func (b searchBranch) writeGraphQL(dst *strings.Builder) {
	switch b.mode {
	case "lexical":
		dst.WriteString("bm25:{query:")
		writeJSONString(dst, b.query)
		dst.WriteString(",properties:")
		writeJSONStrings(dst, b.properties)
		dst.WriteString("},")
	case "semantic":
		dst.WriteString("nearVector:{vector:")
		writeJSONFloat32Slice(dst, b.vector)
		dst.WriteString(",targetVectors:[")
		writeJSONString(dst, b.target)
		dst.WriteString("]},")
	case "hybrid":
		fusion := "relativeScoreFusion"
		if b.ranked {
			fusion = "rankedFusion"
		}
		var num [32]byte
		dst.WriteString("hybrid:{query:")
		writeJSONString(dst, b.query)
		dst.WriteString(",vector:")
		writeJSONFloat32Slice(dst, b.vector)
		dst.WriteString(",alpha:")
		dst.Write(strconv.AppendFloat(num[:0], b.alpha, 'f', -1, 64))
		dst.WriteString(",fusionType:")
		dst.WriteString(fusion)
		dst.WriteString(",properties:")
		writeJSONStrings(dst, b.properties)
		dst.WriteString(",targetVectors:[")
		writeJSONString(dst, b.target)
		dst.WriteString("]},")
	}
}

func (b searchBranch) graphql() string {
	if b.mode != "lexical" && b.mode != "semantic" && b.mode != "hybrid" {
		return ""
	}
	var dst strings.Builder
	dst.Grow(b.encodedLen())
	b.writeGraphQL(&dst)
	return dst.String()
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

func (o searchOrder) writeGraphQL(dst *strings.Builder) {
	if o.field == "" {
		return
	}
	dst.WriteString("sort:[{path:[")
	writeJSONString(dst, o.field)
	if o.desc {
		dst.WriteString("],order:desc}],")
	} else {
		dst.WriteString("],order:asc}],")
	}
}

func (o searchOrder) graphql() string {
	if o.field == "" {
		return ""
	}
	var dst strings.Builder
	dst.Grow(o.encodedLen())
	o.writeGraphQL(&dst)
	return dst.String()
}

func quote(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	writeJSONString(&b, value)
	return b.String()
}

func encodeJSON(value any) string {
	var b strings.Builder
	writeJSONValue(&b, value)
	return b.String()
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

// writeJSONString writes s as a JSON string literal byte-identical to
// encoding/json.Marshal(s) (with HTML escaping for <, >, &, line/paragraph
// separators, control characters, and U+FFFD replacement for invalid UTF-8).
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if safeJSONChar[c] {
			i++
			continue
		}
		if c < utf8.RuneSelf {
			if start < i {
				b.WriteString(s[start:i])
			}
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '<':
				b.WriteString(`\u003c`)
			case '>':
				b.WriteString(`\u003e`)
			case '&':
				b.WriteString(`\u0026`)
			default:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0x0f])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if start < i {
				b.WriteString(s[start:i])
			}
			b.WriteString("\ufffd")
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			if start < i {
				b.WriteString(s[start:i])
			}
			if r == '\u2028' {
				b.WriteString(`\u2028`)
			} else {
				b.WriteString(`\u2029`)
			}
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		b.WriteString(s[start:])
	}
	b.WriteByte('"')
}

// appendJSONString appends s as a JSON string literal byte-identical to
// encoding/json.Marshal(s).
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

func writeJSONStrings(b *strings.Builder, values []string) {
	if values == nil {
		b.WriteString("null")
		return
	}
	b.WriteByte('[')
	for i, s := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(b, s)
	}
	b.WriteByte(']')
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

func writeJSONFloat32Slice(b *strings.Builder, values []float32) {
	if values == nil {
		b.WriteString("null")
		return
	}
	b.WriteByte('[')
	var num [32]byte
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b.WriteString("null")
			continue
		}
		b.Write(appendJSONFloat(num[:0], f, 32))
	}
	b.WriteByte(']')
}

func writeJSONValue(b *strings.Builder, value any) {
	switch v := value.(type) {
	case string:
		writeJSONString(b, v)
	case []string:
		writeJSONStrings(b, v)
	case []float32:
		writeJSONFloat32Slice(b, v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			raw, _ := json.Marshal(v)
			b.Write(raw)
			return
		}
		var num [32]byte
		b.Write(appendJSONFloat(num[:0], v, 64))
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	default:
		raw, _ := json.Marshal(value)
		b.Write(raw)
	}
}
