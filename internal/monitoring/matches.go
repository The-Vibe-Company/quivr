package monitoring

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// Match is an immutable positive historical determination. It is unique per
// Subscription Version and Record Version and does not assert that the
// Record is still current or searchable.
type Match struct {
	ID                    string
	SubscriptionID        string
	SubscriptionVersionID string
	SavedQueryID          string
	SavedQueryVersionID   string
	RecordID              string
	RecordVersionID       string
	PreviousMatchID       string
	Evidence              MatchEvidence
	// Position is the journal position of the Match commit, used for stable paging.
	Position int64
}

// NoticeReferences are the reference-only identifiers of a monitoring notice.
type NoticeReferences struct {
	MatchID               string `json:"match_id"`
	RecordID              string `json:"record_id"`
	RecordVersionID       string `json:"record_version_id"`
	SubscriptionID        string `json:"subscription_id"`
	SubscriptionVersionID string `json:"subscription_version_id"`
	DeliveryID            string `json:"delivery_id"`
	PreviousMatchID       string `json:"previous_match_id,omitempty"`
}

// Notice is the immutable reference-only notification body. Its stored bytes
// are what every delivery attempt sends; the feed event shares its ID.
type Notice struct {
	EventID       string           `json:"event_id"`
	Type          string           `json:"type"`
	SchemaVersion string           `json:"schema_version"`
	OccurredAt    time.Time        `json:"occurred_at"`
	References    NoticeReferences `json:"references"`
}

// Monitoring notice kinds.
const (
	NoticeCreated         = "match.created"
	NoticeCorrected       = "match.corrected"
	NoticeNoLongerMatches = "match.no_longer_matches"
	NoticeWithdrawn       = "match.withdrawn"
)

// AdmissionReason is the canonical admission rule shared by the delivery
// worker and Delivery reads, after the destination check. It returns "" when
// the notice may be attempted. A withdrawn Record refuses every ordinary
// notice, while match.withdrawn has its own eligibility. superseded reports
// that a later match.corrected or match.no_longer_matches exists for the same
// Subscription and Record; it refuses only match.created and match.corrected,
// so a stale positive never lands after its correction. match.no_longer_matches
// and match.withdrawn are never superseded.
func AdmissionReason(kind string, enabled, withdrawn, superseded bool) string {
	switch {
	case !enabled:
		return "subscription_disabled"
	case withdrawn && kind != NoticeWithdrawn:
		return "record_withdrawn"
	case superseded && (kind == NoticeCreated || kind == NoticeCorrected):
		return "superseded"
	}
	return ""
}

// Admission is the current derived admission view of a Delivery.
type Admission struct {
	Allowed bool
	Reason  string
}

// Delivery is the logical notification of one notice to one destination.
type Delivery struct {
	ID             string
	MatchID        string
	SubscriptionID string
	DestinationID  string
	State          string
	AttemptCount   int
	// Event holds the exact immutable notice bytes.
	Event     []byte
	Admission Admission
	// LastOutcome is the latest recorded attempt outcome ("" before any).
	LastOutcome string
	// LastErrorCode and LastErrorMessage describe the latest attempt when it failed.
	LastErrorCode    string
	LastErrorMessage string
	// NextAttemptAt is when the next automatic attempt becomes eligible; nil
	// unless the Delivery is pending and admission is allowed.
	NextAttemptAt *time.Time
}

// MatchStore reads Match history and Deliveries.
type MatchStore interface {
	Match(ctx context.Context, org, id string) (Match, error)
	// Matches returns up to limit Matches of a Subscription committed after position.
	Matches(ctx context.Context, org, subscriptionID string, after int64, limit int) ([]Match, error)
	Delivery(ctx context.Context, org, id string) (Delivery, error)
	// Attempts returns up to limit attempts of a Delivery numbered after after.
	Attempts(ctx context.Context, org, deliveryID string, after, limit int) ([]Attempt, error)
}

// Matches lists a visible Subscription's Match history in commit order.
func (s Service) Matches(ctx context.Context, scope corpus.Scope, subscriptionID string, after int64, limit int) ([]Match, error) {
	if !scope.Allows("monitoring:read") {
		return nil, ErrForbidden
	}
	if _, err := s.visible(ctx, scope, subscriptionID); err != nil {
		return nil, err
	}
	return s.MatchStore.Matches(ctx, scope.Organization, subscriptionID, after, limit)
}

// Match reads one Match when its Subscription and content scope are visible.
func (s Service) Match(ctx context.Context, scope corpus.Scope, id string) (Match, error) {
	if !scope.Allows("monitoring:read") {
		return Match{}, ErrForbidden
	}
	m, err := s.MatchStore.Match(ctx, scope.Organization, id)
	if err != nil {
		return Match{}, err
	}
	if _, err = s.visible(ctx, scope, m.SubscriptionID); err != nil {
		return Match{}, err
	}
	return m, nil
}

// Delivery reads a logical Delivery under the same visibility rule as its Match.
func (s Service) Delivery(ctx context.Context, scope corpus.Scope, id string) (Delivery, error) {
	if !scope.Allows("monitoring:read") {
		return Delivery{}, ErrForbidden
	}
	d, err := s.MatchStore.Delivery(ctx, scope.Organization, id)
	if err != nil {
		return Delivery{}, err
	}
	if _, err = s.visible(ctx, scope, d.SubscriptionID); err != nil {
		return Delivery{}, err
	}
	// The delivery worker refuses a destination no longer configured for the
	// Organization; the admission view says so rather than claiming eligibility.
	if dest, ok := s.Destinations[d.DestinationID]; d.Admission.Allowed && (!ok || dest.Organization != scope.Organization) {
		d.Admission = Admission{Reason: "destination_unavailable"}
		d.NextAttemptAt = nil
	}
	return d, nil
}

// Attempts lists a visible Delivery's append-only attempt history.
func (s Service) Attempts(ctx context.Context, scope corpus.Scope, deliveryID string, after, limit int) ([]Attempt, error) {
	if _, err := s.Delivery(ctx, scope, deliveryID); err != nil {
		return nil, err
	}
	return s.MatchStore.Attempts(ctx, scope.Organization, deliveryID, after, limit)
}
