package httpapi_test

import (
	"context"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/routing"
)

// Transport ownership is admission, authentication and wire shape. Coverage
// and cutover are owned by the real PostgreSQL lifecycle tests.
type routingCommands struct {
	operations.Store
	command routing.Command
	op      operations.Operation
}

func (m *routingCommands) AcceptRouting(_ context.Context, org string, c routing.Command) (operations.Operation, error) {
	m.command = c
	if c.Target == "p.old@1" {
		return operations.Operation{}, backfill.ErrNotEvaluation
	}
	if c.Target == "p.unknown@1" {
		return operations.Operation{}, corpus.ErrNotFound
	}
	m.op = operations.Operation{ID: "operation_admin", Organization: org, Kind: c.Kind, State: operations.StateQueued, Admin: &operations.Admin{Target: c.Target}}
	return m.op, nil
}
func (*routingCommands) StepRouting(context.Context, string, string) (routing.Progress, error) {
	return routing.Progress{}, fmt.Errorf("transport does not execute Operations")
}
func (m *routingCommands) Operation(_ context.Context, org, id string) (operations.Operation, error) {
	if m.op.Organization != org || m.op.ID != id {
		return operations.Operation{}, corpus.ErrNotFound
	}
	return m.op, nil
}
