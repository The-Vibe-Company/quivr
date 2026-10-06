package weaviate

import (
	"context"
	"encoding/json"

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
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	if s.metadataProperties == nil {
		s.metadataProperties = map[string]bool{}
	}
	missing := []corpus.Field{}
	for _, f := range corpus.FilterFields(g.Fields) {
		if !s.metadataProperties[g.Collection+"/"+metadataProperty(f)] {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var class struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	if _, err := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); err != nil {
		return err
	}
	present := map[string]bool{}
	for _, p := range class.Properties {
		present[p.Name] = true
	}
	for _, f := range missing {
		name := metadataProperty(f)
		if !present[name] {
			if _, err := s.call(ctx, "POST", "/v1/schema/"+g.Collection+"/properties", metadataSchema(f), nil); err != nil {
				class.Properties = nil
				if _, readErr := s.call(ctx, "GET", "/v1/schema/"+g.Collection, nil, &class); readErr != nil {
					return readErr
				}
				found := false
				for _, p := range class.Properties {
					if p.Name == name {
						found = true
					}
				}
				if !found {
					return err
				}
			}
		}
		s.metadataProperties[g.Collection+"/"+name] = true
	}
	return nil
}
func metadataCondition(f corpus.TypedFilter) string {
	field := corpus.Field{Name: f.Field, Type: f.Type}
	name := metadataProperty(field)
	key := map[string]string{"string": "valueText", "string_array": "valueText", "datetime": "valueDate", "number": "valueNumber", "boolean": "valueBoolean"}[f.Type]
	condition := func(operator string, value any) string {
		if f.Type == "string" || f.Type == "string_array" {
			value = metadataString(value.(string))
		}
		raw, _ := json.Marshal(value)
		return "{path:[" + quote(name) + "],operator:" + operator + "," + key + ":" + string(raw) + "}"
	}
	operands := []string{}
	if len(f.AnyOf) > 0 {
		values := []string{}
		for _, v := range f.AnyOf {
			values = append(values, condition("Equal", v))
		}
		if len(values) == 1 {
			operands = append(operands, values[0])
		} else {
			operands = append(operands, or(values...))
		}
	}
	if f.Gte != "" {
		operands = append(operands, condition("GreaterThanEqual", f.Gte))
	}
	if f.Lte != "" {
		operands = append(operands, condition("LessThanEqual", f.Lte))
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
