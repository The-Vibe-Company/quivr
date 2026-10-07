package temporal

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"time"
)

// A gate is shared by current and legacy queue pollers of one workload class.
// Capacity is acquired at execution, so idle legacy pollers cannot reserve all
// the class's slots while new workflows wait on another queue.
type capacityGate struct {
	interceptor.WorkerInterceptorBase
	class string
	slots chan struct{}
	bulk  *capacityGate
}

func newCapacityGate(class string, n int) *capacityGate {
	return &capacityGate{class: class, slots: make(chan struct{}, n)}
}
func (g *capacityGate) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &capacityActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, gate: g}
}

type capacityActivity struct {
	interceptor.ActivityInboundInterceptorBase
	gate *capacityGate
}

func (i *capacityActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	gate := i.gate
	name := activity.GetInfo(ctx).ActivityType.Name
	if gate.bulk != nil && name == "rebuild-projection-step" {
		gate = gate.bulk
	}
	ticker := time.NewTicker(stepHeartbeatTimeout / 3)
	defer ticker.Stop()
	for {
		select {
		case gate.slots <- struct{}{}:
			defer func() { <-gate.slots }()
			ctx = workqueue.WithClass(ctx, gate.class)
			switch name {
			case "process-token-windows", "process-token-windows-v2", "normalize-external", "enrich-e5":
				if len(in.Args) > 0 {
					if input, ok := in.Args[0].(Input); ok {
						var result any
						err := workqueue.Track(ctx, input.Organization, "ingestion", input.ReceiptID, input.ReceiptID, func(ctx context.Context) error {
							var err error
							result, err = i.Next.ExecuteActivity(ctx, in)
							return err
						})
						return result, err
					}
				}
			}
			return i.Next.ExecuteActivity(ctx, in)
		case <-ticker.C:
			// Admission waits must not look like a dead activity attempt.
			activity.RecordHeartbeat(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
