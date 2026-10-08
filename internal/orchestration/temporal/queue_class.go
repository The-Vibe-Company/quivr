package temporal

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.temporal.io/sdk/interceptor"
)

// queueClass marks every activity a worker runs with that worker's workload
// class. The worker's activity slots bound the class's capacity.
type queueClass struct {
	interceptor.WorkerInterceptorBase
	class string
}

func (q *queueClass) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &classActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, class: q.class}
}

type classActivity struct {
	interceptor.ActivityInboundInterceptorBase
	class string
}

func (i *classActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	return i.Next.ExecuteActivity(workqueue.WithClass(ctx, i.class), in)
}
