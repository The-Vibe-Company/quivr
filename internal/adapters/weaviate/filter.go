package weaviate

import (
	"github.com/google/uuid"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
)

// The client library exports no NotEqual; the wire operator passes through.
const notEqualOperator = filter.Operator(pb.Filters_OPERATOR_NOT_EQUAL)

func compare(operator filter.Operator, field string, value any) filter.Expr {
	return filter.Cond{Operator: operator, Target: field, Value: value}
}
func equal(field, value string) filter.Expr    { return compare(filter.Equal, field, value) }
func notEqual(field, value string) filter.Expr { return compare(notEqualOperator, field, value) }
func and(operands ...filter.Expr) filter.Expr  { return filter.And(operands) }
func or(operands ...filter.Expr) filter.Expr   { return filter.Or(operands) }

// identity selects one object by its id.
func identity(id uuid.UUID) filter.Expr { return compare(filter.Equal, filter.UUID, id) }
