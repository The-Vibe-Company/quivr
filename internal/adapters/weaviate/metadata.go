package weaviate

import (
	"context"
	"encoding/json"

	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"

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
func metadataSchema(f corpus.Field) collections.Property {
	typ := map[string]collections.DataType{"string": collections.DataTypeText, "string_array": collections.DataTypeTextArray, "datetime": collections.DataTypeDate, "number": collections.DataTypeNumber, "boolean": collections.DataTypeBool}[f.Type]
	property := collections.Property{Name: metadataProperty(f), DataType: typ, IndexFilterable: flag(true), IndexSearchable: flag(false)}
	if f.Type == "string" || f.Type == "string_array" {
		property.Tokenization = collections.TokenizationField
	}
	return property
}

// A cache avoids schema reads on each Version; concurrent processes reconcile
// duplicate property creation by reading the collection again.
func (s *Store) ensureMetadata(ctx context.Context, g content.Generation) error {
	if !g.MetadataProjected {
		return nil
	}
	var props []collections.Property
	for _, f := range corpus.FilterFields(g.Fields) {
		p := metadataSchema(f)
		p.Name = metadataPropertyFor(g, f)
		if g.ItemKeywordsProjected && f.Type == "datetime" {
			p.IndexRangeable = flag(true)
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
func metadataConditionFor(g content.Generation, f corpus.TypedFilter) filter.Expr {
	return metadataCondition(f, metadataPropertyFor(g, corpus.Field{Name: f.Field, Type: f.Type}))
}

func metadataCondition(f corpus.TypedFilter, name string) filter.Expr {
	condition := func(operator filter.Operator, value any) filter.Expr {
		if f.Type == "string" || f.Type == "string_array" {
			value = metadataString(value.(string))
		}
		return compare(operator, name, value)
	}
	operands := []filter.Expr{}
	if len(f.AnyOf) > 0 {
		values := []filter.Expr{}
		for _, v := range f.AnyOf {
			values = append(values, condition(filter.Equal, v))
		}
		if len(values) == 1 {
			operands = append(operands, values[0])
		} else {
			operands = append(operands, or(values...))
		}
	}
	if f.Gte != "" {
		operands = append(operands, condition(filter.GreaterThanEqual, f.Gte))
	}
	if f.Lte != "" {
		operands = append(operands, condition(filter.LessThanEqual, f.Lte))
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
