package temporal

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
)

type correlationInterceptor struct{ interceptor.InterceptorBase }

func (correlationInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &correlatedWorkflow{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

type correlatedWorkflow struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (w *correlatedWorkflow) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return w.Next.Init(&correlatedWorkflowOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

type correlatedWorkflowOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

func (w *correlatedWorkflowOutbound) GetLogger(ctx workflow.Context) log.Logger {
	id, _ := ctx.Value(workflowRequestID{}).(string)
	return log.With(w.Next.GetLogger(ctx), "request_id", id)
}
func (correlationInterceptor) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &correlatedActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}}
}

type correlatedActivity struct {
	interceptor.ActivityInboundInterceptorBase
}

func (a *correlatedActivity) Init(next interceptor.ActivityOutboundInterceptor) error {
	return a.Next.Init(&correlatedActivityOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: next}})
}

type correlatedActivityOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
}

func (a *correlatedActivityOutbound) GetLogger(ctx context.Context) log.Logger {
	return log.With(a.Next.GetLogger(ctx), "request_id", logging.RequestID(ctx))
}
