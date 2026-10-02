package plugins

import "time"

const (
	NormalizerTimeoutCap      = 2 * time.Minute
	SegmentAndEmbedTimeoutCap = 5 * time.Minute
	SubscriptionTimeoutCap    = 30 * time.Second
	ConnectorTimeoutCap       = 30 * time.Second
	ReceiveTimeoutCap         = 8 * time.Second
)

// InvocationTimeout is the invocation policy, also used to keep signed input references
// valid past a normalization. A search round inherits its whole-search bound.
func InvocationTimeout(pin *Pin, operation string) time.Duration {
	m := pin.Manifest.Contributions
	cap, declared := time.Duration(0), 0
	switch operation {
	case "normalize":
		cap, declared = NormalizerTimeoutCap, m.Normalizer.TimeoutMS
	case "segment_and_embed":
		cap, declared = SegmentAndEmbedTimeoutCap, m.Ingestion.TimeoutMS
	case "embed_query":
		return time.Duration(m.Ingestion.QueryTimeoutMS) * time.Millisecond
	case "evaluate_subscription":
		cap, declared = SubscriptionTimeoutCap, m.Subscription.TimeoutMS
	case "connector_fetch", "check_credential":
		cap, declared = ConnectorTimeoutCap, m.Connector.TimeoutMS
	case "connector_receive":
		cap, declared = ReceiveTimeoutCap, m.Connector.TimeoutMS
	case "describe_attachment", "upload_attachment":
		return time.Duration(AttachmentTimeoutMS(&pin.Manifest)) * time.Millisecond
	case "search_round":
		bound := (RetrievalProfile{}).Deadline()
		if m.Retrieval != nil {
			for _, p := range m.Retrieval.Profiles {
				bound = max(bound, p.Deadline())
			}
		}
		return bound
	}
	d := time.Duration(declared) * time.Millisecond
	if d <= 0 || d > cap {
		return cap
	}
	return d
}
