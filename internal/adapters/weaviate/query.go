package weaviate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
	return filter{operator: pb.Filters_OPERATOR_AND, children: operands}
}

func or(operands ...filter) filter {
	return filter{operator: pb.Filters_OPERATOR_OR, children: operands}
}

func containsAny(field string, values []string) filter {
	return filter{operator: pb.Filters_OPERATOR_CONTAINS_ANY, field: field, jsonKey: "valueText", value: values}
}

var graphOperators = map[pb.Filters_Operator]string{
	pb.Filters_OPERATOR_EQUAL: "Equal", pb.Filters_OPERATOR_NOT_EQUAL: "NotEqual",
	pb.Filters_OPERATOR_AND: "And", pb.Filters_OPERATOR_OR: "Or",
	pb.Filters_OPERATOR_CONTAINS_ANY:       "ContainsAny",
	pb.Filters_OPERATOR_GREATER_THAN_EQUAL: "GreaterThanEqual", pb.Filters_OPERATOR_LESS_THAN_EQUAL: "LessThanEqual",
}

func (f filter) String() string {
	operator := graphOperators[f.operator]
	if f.field == "" {
		children := make([]string, 0, len(f.children))
		for _, child := range f.children {
			children = append(children, child.String())
		}
		return "{operator:" + operator + ",operands:[" + strings.Join(children, ",") + "]}"
	}
	return "{path:[" + quote(f.field) + "],operator:" + operator + "," + f.jsonKey + ":" + encodeJSON(f.value) + "}"
}

func (f filter) proto() *pb.Filters {
	out := &pb.Filters{Operator: f.operator}
	if f.field == "" {
		for _, child := range f.children {
			out.Filters = append(out.Filters, child.proto())
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

func (b searchBranch) graphql() string {
	switch b.mode {
	case "lexical":
		return fmt.Sprintf("bm25:{query:%s,properties:%s},", quote(b.query), encodeJSON(b.properties))
	case "semantic":
		return fmt.Sprintf("nearVector:{vector:%s,targetVectors:[%s]},", encodeJSON(b.vector), quote(b.target))
	case "hybrid":
		fusion := "relativeScoreFusion"
		if b.ranked {
			fusion = "rankedFusion"
		}
		return fmt.Sprintf("hybrid:{query:%s,vector:%s,alpha:%s,fusionType:%s,properties:%s,targetVectors:[%s]},",
			quote(b.query), encodeJSON(b.vector), strconv.FormatFloat(b.alpha, 'f', -1, 64), fusion, encodeJSON(b.properties), quote(b.target))
	}
	return ""
}

type searchOrder struct {
	field string
	desc  bool
}

func (o searchOrder) graphql() string {
	if o.field == "" {
		return ""
	}
	direction := "asc"
	if o.desc {
		direction = "desc"
	}
	return fmt.Sprintf("sort:[{path:[%s],order:%s}],", quote(o.field), direction)
}

func quote(value string) string { return encodeJSON(value) }

func encodeJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
