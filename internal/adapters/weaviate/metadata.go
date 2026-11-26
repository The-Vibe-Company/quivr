package weaviate

import (
	"context"
	"encoding/json"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// A logical name and type identify a private property; different types in
// different Corpora never share a physical schema property.
func metadataProperty(field corpus.Field) string {
	return "m_" + content.Hash([]byte(field.Name + "\x00" + field.Type))[:32]
}
func metadataString(value string) string { return content.Hash([]byte(value)) }
func metadataValue(value any, typ string) any {
	switch typ {
	case "string":
		return metadataString(value.(string))
	case "string_array":
		items := value.([]string)
		out := make([]string, len(items))
		for i, v := range items {
			out[i] = metadataString(v)
		}
		return out
	}
	return value
}
func metadataSchema(f corpus.Field) map[string]any {
	name := metadataProperty(f)
	typ := map[string]string{"string": "text", "string_array": "text[]", "datetime": "date", "number": "number", "boolean": "boolean"}[f.Type]
	property := map[string]any{"name": name, "dataType": []string{typ}, "indexFilterable": true, "indexSearchable": false}
	if f.Type == "string" || f.Type == "string_array" {
		property["tokenization"] = "field"
	}
	return property
}

// A cache avoids schema reads on each Version; concurrent processes reconcile
// duplicate property creation by reading the collection again.
func (s *Store) ensureMetadata(ctx context.Context, g content.Generation) error {
	if !g.MetadataProjected {
		return nil
	}
	var props []map[string]any
	for _, f := range corpus.FilterFields(g.Fields) {
		p := metadataSchema(f)
		p["name"] = metadataPropertyFor(g, f)
		if g.ItemKeywordsProjected && f.Type == "datetime" {
			p["indexRangeFilters"] = true
		}
		props = append(props, p)
	}
	return s.ensureProperties(ctx, g.Collection, props)
}
func metadataPropertyFor(g content.Generation, f corpus.Field) string {
	if g.ItemKeywordsProjected && f.Type == "datetime" {
		// Domain separation cannot collide with a valid legacy logical name.
		return "m_" + content.Hash([]byte(f.Name + "\x00" + f.Type + "\x00range_v1"))[:32]
	}
	return metadataProperty(f)
}
func metadataConditionFor(g content.Generation, f corpus.TypedFilter) filter {
	return metadataCondition(f, metadataPropertyFor(g, corpus.Field{Name: f.Field, Type: f.Type}))
}

func metadataCondition(f corpus.TypedFilter, name string) filter {
	key := map[string]string{"string": "valueText", "string_array": "valueText", "datetime": "valueDate", "number": "valueNumber", "boolean": "valueBoolean"}[f.Type]
	condition := func(operator pb.Filters_Operator, value any) filter {
		if f.Type == "string" || f.Type == "string_array" {
			value = metadataString(value.(string))
		}
		return filter{operator: operator, field: name, jsonKey: key, value: value}
	}
	var operands []filter
	if len(f.AnyOf) > 0 {
		var values []filter
		for _, v := range f.AnyOf {
			values = append(values, condition(pb.Filters_OPERATOR_EQUAL, v))
		}
		if len(values) == 1 {
			operands = append(operands, values[0])
		} else {
			operands = append(operands, or(values...))
		}
	}
	if f.Gte != "" {
		operands = append(operands, condition(pb.Filters_OPERATOR_GREATER_THAN_EQUAL, f.Gte))
	}
	if f.Lte != "" {
		operands = append(operands, condition(pb.Filters_OPERATOR_LESS_THAN_EQUAL, f.Lte))
	}
	if len(operands) == 1 {
		return operands[0]
	}
	return and(operands...)
}
func sameMetadata(stored, want any) bool {
	a, _ := json.Marshal(stored)
	b, _ := json.Marshal(want)
	return string(a) == string(b)
}
