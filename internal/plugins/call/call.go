// Package call owns the policy of an engine invocation of a pinned plugin.
// Activity and whole-search deadlines remain outer envelopes; each invocation
// checks its pin and runs within the contribution's deadline inside them.
package call

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const (
	Normalize            = "normalize"
	SegmentAndEmbed      = "segment_and_embed"
	EmbedQuery           = "embed_query"
	SearchRound          = "search_round"
	EvaluateSubscription = "evaluate_subscription"
	ConnectorFetch       = "connector_fetch"
	CheckCredential      = "check_credential"
	ConnectorReceive     = "connector_receive"
	DescribeAttachment   = "describe_attachment"
	UploadAttachment     = "upload_attachment"
)

// Build creates a request after discovery, using the API the peer actually
// serves. ctx carries the invocation deadline, including discovery.
type Build func(context.Context, string) ([]byte, error)

// Check applies contribution-specific output validation inside the deadline.
type Check func(context.Context, []byte) []plugins.Issue

// Timeout is the shared policy for invocation and input-reference lifetime.
func Timeout(pin *plugins.Pin, operation string) time.Duration {
	return plugins.InvocationTimeout(pin, operation)
}

// Discover refuses cancelled/stopped work and verifies the installed digest,
// identity and protocol before a caller prepares expensive input references.
func Discover(ctx context.Context, pin *plugins.Pin) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout(pin, Normalize))
	defer cancel()
	return discover(ctx, pin)
}

func discover(ctx context.Context, pin *plugins.Pin) (string, error) {
	if err := guard(ctx, pin); err != nil {
		return "", err
	}
	ctx, err := plugins.SigningContext(ctx, pin)
	if err != nil {
		return "", fmt.Errorf("%w: %w", plugins.ErrUnavailable, err)
	}
	served, issues, err := devhost.Discover(ctx, pin.Endpoint, pin.Report())
	if err != nil {
		return "", fmt.Errorf("%w: %v", plugins.ErrUnavailable, err)
	}
	if len(issues) > 0 {
		return "", fmt.Errorf("%w: discovery does not match the pinned manifest: %s", plugins.ErrUnavailable, plugins.DescribeIssues(issues))
	}
	return served, nil
}

func guard(ctx context.Context, pin *plugins.Pin) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", plugins.ErrUnavailable, err)
	}
	if plugins.Stopped(ctx, pin) {
		return fmt.Errorf("%w: a rollback stopped this work, and %s@%s has left the active plan", plugins.ErrUnavailable, pin.Manifest.ID, pin.Manifest.Version)
	}
	return nil
}

// Invoke is the only engine path to a Contribution. It owns stop checks,
// discovery/digest checks, deadlines, stable keys and result/error classification.
func Invoke(ctx context.Context, pin *plugins.Pin, operation string, build Build, check Check, secrets []string) (*devhost.Result, error) {
	invoke := ctx
	cancel := func() {}
	if timeout := Timeout(pin, operation); timeout > 0 {
		invoke, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	served, err := discover(invoke, pin)
	if err != nil {
		return nil, invocationFailure(ctx, invoke, operation, err)
	}
	body, err := build(invoke, served)
	if err != nil {
		if invoke.Err() != nil {
			return nil, invocationFailure(ctx, invoke, operation, err)
		}
		return nil, err
	}
	body, err = identify(pin, operation, body)
	if err != nil {
		if invoke.Err() != nil {
			return nil, invocationFailure(ctx, invoke, operation, err)
		}
		return nil, err
	}
	if err = guard(invoke, pin); err != nil {
		return nil, invocationFailure(ctx, invoke, operation, err)
	}
	invoke, err = plugins.SigningContext(invoke, pin)
	if err != nil {
		return nil, invocationFailure(ctx, invoke, operation, err)
	}
	validate := func(b []byte) []plugins.Issue {
		if check == nil {
			return nil
		}
		return check(invoke, b)
	}
	m := &pin.Manifest
	var result *devhost.Result
	switch operation {
	case Normalize:
		result, err = devhost.InvokeNormalizerWith(invoke, pin.Endpoint, body, plugins.MaxResponseBytes(m), validate)
	case SegmentAndEmbed:
		result, err = devhost.InvokeSegmentAndEmbed(invoke, pin.Endpoint, body, plugins.IngestionMaxResponseBytes(m), validate)
	case EmbedQuery:
		result, err = devhost.InvokeEmbedQuery(invoke, pin.Endpoint, body, validate)
	case SearchRound:
		result, err = devhost.InvokeSearch(invoke, pin.Endpoint, body, plugins.RetrievalMaxResponseBytes(m), validate)
	case EvaluateSubscription:
		result, err = devhost.InvokeSubscriptionWith(invoke, pin.Endpoint, body, plugins.SubscriptionMaxResponseBytes(m), validate)
	case ConnectorFetch:
		result, err = devhost.InvokeConnectorFetch(invoke, pin.Endpoint, body, plugins.ConnectorMaxResponseBytes(m), validate)
	case CheckCredential:
		result, err = devhost.InvokeCheckCredential(invoke, pin.Endpoint, body)
	case ConnectorReceive:
		result, err = devhost.InvokeConnectorReceive(invoke, pin.Endpoint, body, plugins.ConnectorMaxResponseBytes(m), validate)
	case DescribeAttachment:
		result, err = devhost.InvokeDescribeAttachment(invoke, pin.Endpoint, body, validate)
	case UploadAttachment:
		result, err = devhost.InvokeUploadAttachment(invoke, pin.Endpoint, body)
	default:
		return nil, fmt.Errorf("unknown plugin operation %q", operation)
	}
	if err != nil {
		return result, invocationFailure(ctx, invoke, operation, err)
	}
	if err := guard(invoke, pin); err != nil {
		return result, invocationFailure(ctx, invoke, operation, err)
	}
	return result, judge(pin, operation, result, secrets)
}

// Every phase shares the same deadline accounting. Parent cancellation belongs
// to the activity/search envelope; only our inner expiry consumes plugin budget.
func invocationFailure(parent, invoke context.Context, operation string, err error) error {
	if parent.Err() == nil && errors.Is(invoke.Err(), context.DeadlineExceeded) {
		err = errors.Join(plugins.ErrCallDeadline, err)
	}
	return unavailable(operation, err)
}

func connectorOperation(operation string) bool {
	switch operation {
	case ConnectorFetch, CheckCredential, ConnectorReceive, DescribeAttachment, UploadAttachment:
		return true
	}
	return false
}
func unavailable(operation string, err error) error {
	if connectorOperation(operation) {
		return connectors.TransientError("plugin_unavailable")
	}
	if operation == EvaluateSubscription {
		return fmt.Errorf("%w: %w", monitoring.ErrEvaluatorUnavailable, err)
	}
	return fmt.Errorf("%w: %w", plugins.ErrUnavailable, err)
}

// judge keeps operation-specific public semantics in the same module as the
// shared transport classification. Callers only decode checked success data.
func judge(pin *plugins.Pin, operation string, result *devhost.Result, secrets []string) error {
	if result == nil {
		return unavailable(operation, plugins.ErrUnavailable)
	}
	if connectorOperation(operation) {
		if plugins.ContainsSecret(result.Body, secrets) {
			return connectors.SourceError("credential_leak")
		}
		if e := result.Error; e != nil {
			if len(plugins.ConnectorErrorIssues(e.Class, e.Retryable)) > 0 {
				return connectors.SourceError("plugin_invalid_error")
			}
			return &connectors.Error{Class: connectors.ErrorClass(e.Class), Code: e.Code, RetryAfter: time.Duration(e.RetryAfterSeconds) * time.Second}
		}
		if len(result.Issues) > 0 {
			if result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope {
				return unavailable(operation, plugins.ErrUnavailable)
			}
			if result.Issues[0].Code == plugins.CodeAttachmentsUnsupported {
				return connectors.SourceError("attachments_unsupported")
			}
			return connectors.SourceError("plugin_invalid_response")
		}
		return nil
	}
	if e := result.Error; e != nil {
		declared := &plugins.PluginError{Status: result.Status, Code: plugins.BoundedDiagnostic(e.Code, 256), Message: plugins.BoundedDiagnostic(e.Message, 1000), Retryable: e.Retryable}
		switch operation {
		case EvaluateSubscription:
			if e.Retryable {
				return errors.Join(monitoring.ErrEvaluation, monitoring.ErrEvaluationRetryable, declared)
			}
			return errors.Join(monitoring.ErrEvaluation, declared)
		case SegmentAndEmbed:
			if !e.Retryable {
				return refused(pin, "%s@%s refused it (%s): %s", pin.Manifest.ID, pin.Manifest.Version, plugins.BoundedDiagnostic(e.Code, 256), plugins.BoundedDiagnostic(e.Message, 256))
			}
		case EmbedQuery:
			if !e.Retryable {
				if e.Code == "query_too_long" {
					return publicerr.WithDetail(retrieval.ErrQueryTooLong, "%s", plugins.BoundedDiagnostic(e.Message, 256))
				}
				return content.ErrInvalid
			}
		case SearchRound:
			if !e.Retryable {
				return fmt.Errorf("%w: %s: %s", content.ErrInvalid, declared.Code, declared.Message)
			}
		}
		return declared
	}
	if len(result.Issues) > 0 {
		if operation == SearchRound && result.Issues[0].Code == devhost.CodeResponseTooLarge {
			return fmt.Errorf("%w: %s", retrieval.ErrPluginInvalid, plugins.DescribeIssues(result.Issues))
		}
		if result.Status != 200 || result.Issues[0].Code == devhost.CodeInvalidErrorEnvelope || operation == SearchRound {
			return unavailable(operation, fmt.Errorf("%s", plugins.DescribeIssues(result.Issues)))
		}
		switch operation {
		case SegmentAndEmbed, EmbedQuery:
			return refused(pin, "%s@%s gave an answer the engine refuses: %s", pin.Manifest.ID, pin.Manifest.Version, plugins.BoundedDiagnostic(plugins.DescribeIssues(result.Issues), 256))
		case EvaluateSubscription:
			return fmt.Errorf("%w: %s", monitoring.ErrEvaluationInvalid, plugins.DescribeIssues(result.Issues))
		default:
			return &plugins.InvalidOutput{Issues: result.Issues}
		}
	}
	return nil
}
func refused(pin *plugins.Pin, format string, args ...any) error {
	return &content.Refusal{Reason: content.Diagnostic{Code: content.ErrIngestionRefused.Error(), Message: plugins.BoundedDiagnostic(fmt.Sprintf(format, args...), 1000), Plugin: pin.Manifest.ID, PluginVersion: pin.Manifest.Version, Contribution: "ingestion"}}
}

// Bytes adapts an already built request for operations independent of discovery.
func Bytes(body []byte) Build {
	return func(context.Context, string) ([]byte, error) { return body, nil }
}

// identify derives logical work identity at the invocation seam. Callers may
// record that identity as provenance, but cannot send a key for another input.
func identify(pin *plugins.Pin, operation string, body []byte) ([]byte, error) {
	switch operation {
	case Normalize:
		var r plugins.NormalizerRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		r.IdempotencyKey = plugins.NormalizerKey(pin.Generation(), "normalizer", r.OrganizationID, r.RecordVersionID, r.Input.SHA256)
		return plugins.BuildNormalizerRequest(r)
	case SegmentAndEmbed:
		var r plugins.SegmentAndEmbedRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		r.IdempotencyKey = plugins.IngestionKey(pin.Generation(), r.OrganizationID, r.Version.RecordVersionID, r.Spaces)
		return plugins.BuildSegmentAndEmbedRequest(r)
	case EvaluateSubscription:
		var r plugins.SubscriptionRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		r.IdempotencyKey = plugins.SubscriptionKey(plugins.SubscriptionKeyInput{Generation: pin.Generation(), OrganizationID: r.OrganizationID, VersionID: r.Record.RecordVersionID, Enriched: r.Record.Enriched, Evaluations: r.Evaluations, IncludeVectors: pin.Manifest.Contributions.Subscription.Vectors != nil, VectorSpaceID: r.Record.VectorSpaceID, VectorsReady: r.Record.VectorsReady})
		return plugins.BuildSubscriptionRequest(r)
	default:
		return body, nil
	}
}
