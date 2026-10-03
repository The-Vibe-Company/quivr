package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

var (
	ErrConflict          = publicerr.IdempotencyConflict
	ErrNamespaceInUse    = publicerr.SourceNamespaceInUse
	ErrUnsupportedKind   = publicerr.UnsupportedConnectorKind
	ErrInvalidConfig     = publicerr.InvalidConfig
	ErrInvalidCredential = publicerr.InvalidCredential
	ErrInvalidInterval   = publicerr.InvalidInterval
	ErrInvalid           = publicerr.InvalidInput
	ErrDisabled          = publicerr.ConnectorDisabled
	// ErrCredentialsUnavailable refuses a credential deposit or rotation on a
	// deployment without credential_key, before the secret is digested or stored.
	ErrCredentialsUnavailable = publicerr.CredentialsUnavailable
)

// DefaultMinInterval is the product floor for polling intervals.
const DefaultMinInterval = 30 * time.Second

// Instance is a Connector Instance as seen by authorized readers. It never
// carries a secret.
type Instance struct {
	Organization      string
	ID                string
	CorpusID          string
	Namespace         string
	Kind              string
	Config            json.RawMessage
	PushPolicy        *PushPolicy
	Interval          time.Duration
	SilentAfter       time.Duration
	CredentialWarning time.Duration
	Enabled           bool
	CreatedAt         time.Time
	DisabledAt        *time.Time
	Credential        *CredentialInfo
	Health            Health
}

// CredentialInfo is the metadata of the current Deposited Credential.
type CredentialInfo struct {
	Version     int
	DepositedAt time.Time
	ExpiresAt   *time.Time
}

// Health is the last committed Connector Health.
type Health struct {
	State         string
	EvaluatedAt   time.Time
	LastSuccessAt *time.Time
	LastItemAt    *time.Time
	LastError     *RunError
	// AccessErrorAt is internal: the unresolved access refusal, if any.
	AccessErrorAt *time.Time
	// Usage is nil for kinds that never report source reads.
	Usage *Usage
	// Diagnostics is the kind-defined object of the latest committed page.
	Diagnostics json.RawMessage
	// Push is nil until a push kind reports its push channel.
	Push *PushHealth
}

// Usage counts source resources read per UTC day (current and previous).
type Usage struct {
	Day                  time.Time
	ItemsRead            int64
	PreviousDayItemsRead int64
}

// NewInstance is a validated creation request ready to persist.
type NewInstance struct {
	Instance
	RequestKey    string
	RequestDigest []byte
	Credential    *Sealed
}

// CredentialDeposit is a validated credential replacement ready to persist.
type CredentialDeposit struct {
	RequestKey    string
	RequestDigest []byte
	Sealed        Sealed
}

// Store persists Connector Instances. Every mutation commits its change event
// and re-evaluated health in the same transaction.
type Store interface {
	CreateConnector(context.Context, NewInstance) (Instance, error)
	ReadConnector(ctx context.Context, org, id string) (Instance, error)
	ListConnectors(ctx context.Context, scope corpus.Scope, corpusID, after string, limit int) ([]Instance, error)
	DisableConnector(ctx context.Context, org, id string) (Instance, error)
	ReplaceCredential(ctx context.Context, org, id string, deposit CredentialDeposit) (Instance, error)
	// ChangeSchedule sets the interval of an enabled instance (ErrDisabled
	// otherwise), committing connector.schedule_changed only on a change.
	ChangeSchedule(ctx context.Context, org, id string, interval time.Duration) (Instance, error)
	// RequestRun pulls the next run of an enabled instance in (ErrDisabled
	// otherwise) to now, but never before floor after the last run ended nor
	// before the source's Retry-After, and never later than already
	// scheduled. It returns when the run is due.
	RequestRun(ctx context.Context, org, id string, floor time.Duration) (time.Time, error)
}

// Service authorizes and validates Connector Instance commands.
type Service struct {
	Store       Store
	Tokens      TokenStore
	Registry    *Registry
	Sealer      Sealer
	MinInterval time.Duration
	// PublicURL is the deployment's public base URL, from which push
	// instances get their webhook address; "" gives them none.
	PublicURL string
}

// WebhookURL is the public webhook address of an instance whose kind
// declares the push mode, or "".
func (s Service) WebhookURL(in Instance) string {
	c, ok := s.Registry.Lookup(in.Kind)
	if !ok || c.Descriptor().Receiver == nil {
		return ""
	}
	return receiverWebhookURL(c, s.PublicURL, in.ID)
}

// CreateInput is a creation command. Secret is the raw kind-specific secret.
type CreateInput struct {
	Key                      string          `json:"idempotency_key"`
	CorpusID                 string          `json:"corpus_id"`
	Namespace                string          `json:"source_namespace"`
	Kind                     string          `json:"kind"`
	Config                   json.RawMessage `json:"config"`
	PushPolicy               *PushPolicy     `json:"push_policy,omitempty"`
	IntervalSeconds          *int            `json:"interval_seconds,omitempty"`
	SilentAfterSeconds       *int            `json:"silent_after_seconds,omitempty"`
	CredentialWarningSeconds *int            `json:"credential_warning_seconds,omitempty"`
	Secret                   json.RawMessage `json:"secret,omitempty"`
	ExpiresAt                *time.Time      `json:"expires_at,omitempty"`
}

// CredentialInput replaces the current credential.
type CredentialInput struct {
	Key       string          `json:"idempotency_key"`
	Secret    json.RawMessage `json:"secret"`
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
}

func validText(s string) bool { return s != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// Create validates and persists a Connector Instance; a replay returns it.
func (s Service) Create(ctx context.Context, scope corpus.Scope, in CreateInput, prepare ...func() (CreateInput, error)) (Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsCreate); err != nil {
		return Instance{}, err
	}
	for _, load := range prepare {
		var err error
		in, err = load()
		if err != nil {
			return Instance{}, err
		}
	}
	if !scope.Contains(in.CorpusID) {
		return Instance{}, corpus.ErrNotFound
	}
	for _, f := range []struct{ value, at string }{{in.Key, "/idempotency_key"}, {in.Namespace, "/source_namespace"}, {in.CorpusID, "/corpus_id"}} {
		if !validText(f.value) {
			return Instance{}, WithField(ErrInvalid, f.at)
		}
	}
	if in.Secret != nil && !s.Sealer.CanSeal() {
		return Instance{}, ErrCredentialsUnavailable
	}
	connector, ok := s.Registry.current()[in.Kind]
	if !ok {
		return Instance{}, ErrUnsupportedKind
	}
	if in.PushPolicy != nil && in.PushPolicy.Validate() != nil {
		return Instance{}, WithField(ErrInvalidConfig, "/push_policy")
	}
	if err := connector.validate(in.Config, in.Secret, "/credential/secret"); err != nil {
		return Instance{}, err
	}
	descriptor := connector.Descriptor()
	if cc := descriptor.Config; cc != nil && cc.CheckConfig(in.Config, time.Now()) != nil {
		return Instance{}, WithField(ErrInvalidConfig, "/config")
	}
	interval := descriptor.DefaultInterval
	if in.IntervalSeconds != nil {
		interval = time.Duration(*in.IntervalSeconds) * time.Second
	}
	if !s.validInterval(interval) {
		return Instance{}, WithField(ErrInvalidInterval, "/schedule/interval_seconds")
	}
	silent, warning := DefaultSilentAfter, DefaultCredentialWarning
	if in.SilentAfterSeconds != nil {
		silent = time.Duration(*in.SilentAfterSeconds) * time.Second
	}
	if in.CredentialWarningSeconds != nil {
		warning = time.Duration(*in.CredentialWarningSeconds) * time.Second
	}
	if silent <= 0 || warning < 0 {
		return Instance{}, WithField(ErrInvalid, "/health_policy")
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return Instance{}, ErrInvalid
	}
	digest, err := s.Sealer.Digest("create", canonical)
	if err != nil {
		return Instance{}, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Instance{}, err
	}
	n := NewInstance{Instance: Instance{Organization: scope.Organization, ID: "connector_" + hex.EncodeToString(id[:]), CorpusID: in.CorpusID, Namespace: in.Namespace, Kind: in.Kind, Config: in.Config, PushPolicy: in.PushPolicy, Interval: interval, SilentAfter: silent, CredentialWarning: warning, Enabled: true}, RequestKey: in.Key, RequestDigest: digest}
	if in.Secret != nil {
		sealed, err := s.Sealer.Seal(scope.Organization, n.ID, in.Secret)
		if err != nil {
			return Instance{}, err
		}
		sealed.ExpiresAt = in.ExpiresAt
		n.Credential = &sealed
	}
	return s.Store.CreateConnector(ctx, n)
}

// Read returns an authorized instance; any other is not found.
func (s Service) Read(ctx context.Context, scope corpus.Scope, id string) (Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsRead); err != nil {
		return Instance{}, err
	}
	return s.authorized(ctx, scope, id)
}

func (s Service) authorized(ctx context.Context, scope corpus.Scope, id string) (Instance, error) {
	inst, err := s.Store.ReadConnector(ctx, scope.Organization, id)
	if err == nil && !scope.Contains(inst.CorpusID) {
		return Instance{}, corpus.ErrNotFound
	}
	return inst, err
}

// List returns authorized instances after a keyset position.
func (s Service) List(ctx context.Context, scope corpus.Scope, corpusID, after string, limit int, prepare ...func() (string, string, int, error)) ([]Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsList); err != nil {
		return nil, err
	}
	for _, load := range prepare {
		var err error
		corpusID, after, limit, err = load()
		if err != nil {
			return nil, err
		}
	}
	if corpusID != "" && !scope.Contains(corpusID) {
		return []Instance{}, nil
	}
	return s.Store.ListConnectors(ctx, scope, corpusID, after, limit)
}

// Disable stops scheduling; it is idempotent and absorbing.
func (s Service) Disable(ctx context.Context, scope corpus.Scope, id string, prepare ...func() (string, error)) (Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsDisable); err != nil {
		return Instance{}, err
	}
	for _, load := range prepare {
		var err error
		id, err = load()
		if err != nil {
			return Instance{}, err
		}
	}
	if _, err := s.authorized(ctx, scope, id); err != nil {
		return Instance{}, err
	}
	return s.Store.DisableConnector(ctx, scope.Organization, id)
}

// ReplaceCredential deposits a new credential version.
func (s Service) ReplaceCredential(ctx context.Context, scope corpus.Scope, id string, in CredentialInput, prepare ...func() (string, CredentialInput, error)) (Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsReplaceCredential); err != nil {
		return Instance{}, err
	}
	for _, load := range prepare {
		var err error
		id, in, err = load()
		if err != nil {
			return Instance{}, err
		}
	}
	inst, err := s.authorized(ctx, scope, id)
	if err != nil {
		return Instance{}, err
	}
	if !validText(in.Key) {
		return Instance{}, WithField(ErrInvalid, "/idempotency_key")
	}
	// A disabled instance is refused by the store after replay detection, so a
	// retried rotation that already succeeded still replays.
	if in.Secret == nil {
		return Instance{}, WithField(ErrInvalidCredential, "/secret")
	}
	if !s.Sealer.CanSeal() {
		return Instance{}, ErrCredentialsUnavailable
	}
	if err = s.Registry.validate(inst.Kind, inst.Config, in.Secret, "/secret"); err != nil {
		return Instance{}, err
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return Instance{}, ErrInvalid
	}
	digest, err := s.Sealer.Digest("credential", canonical)
	if err != nil {
		return Instance{}, err
	}
	sealed, err := s.Sealer.Seal(scope.Organization, id, in.Secret)
	if err != nil {
		return Instance{}, err
	}
	sealed.ExpiresAt = in.ExpiresAt
	return s.Store.ReplaceCredential(ctx, scope.Organization, id, CredentialDeposit{RequestKey: in.Key, RequestDigest: digest, Sealed: sealed})
}

// Kinds describes the enabled kinds and whether credentials can be deposited.
func (s Service) Kinds(scope corpus.Scope) (Catalog, error) {
	if err := scope.Require(corpus.ActionConnectorsKinds); err != nil {
		return Catalog{}, err
	}
	return Catalog{Kinds: s.Registry.Describe(), CredentialDeposits: s.Sealer.CanSeal(), MinInterval: s.minInterval()}, nil
}

// ChangeSchedule sets the polling interval. Setting the current value is a
// no-op, so repeating the request is harmless.
func (s Service) ChangeSchedule(ctx context.Context, scope corpus.Scope, id string, seconds int, prepare ...func() (string, int, error)) (Instance, error) {
	if err := scope.Require(corpus.ActionConnectorsChangeSchedule); err != nil {
		return Instance{}, err
	}
	for _, load := range prepare {
		var err error
		id, seconds, err = load()
		if err != nil {
			return Instance{}, err
		}
	}
	if _, err := s.authorized(ctx, scope, id); err != nil {
		return Instance{}, err
	}
	// Bound before converting: a huge value would wrap into a valid Duration.
	interval := time.Duration(seconds) * time.Second
	if seconds < 1 || seconds > 86400 || !s.validInterval(interval) {
		return Instance{}, WithField(ErrInvalidInterval, "/interval_seconds")
	}
	return s.Store.ChangeSchedule(ctx, scope.Organization, id, interval)
}

// RunRequest answers a run request: the run is due at RunAt, and the
// scheduler starts it on its next claim after that.
type RunRequest struct {
	ConnectorID string
	RunAt       time.Time
}

// RequestRun asks for a run now instead of at the next scheduled time. The
// deployment floor still separates two runs and a source's Retry-After still
// holds, so a request cannot poll a source faster than a schedule could.
// Repeating it changes nothing; a run already in flight answers it.
func (s Service) RequestRun(ctx context.Context, scope corpus.Scope, id, key string, prepare ...func() (string, string, error)) (RunRequest, error) {
	if err := scope.Require(corpus.ActionConnectorsRequestRun); err != nil {
		return RunRequest{}, err
	}
	for _, load := range prepare {
		var err error
		id, key, err = load()
		if err != nil {
			return RunRequest{}, err
		}
	}
	if _, err := s.authorized(ctx, scope, id); err != nil {
		return RunRequest{}, err
	}
	if !validText(key) {
		return RunRequest{}, WithField(ErrInvalid, "/idempotency_key")
	}
	at, err := s.Store.RequestRun(ctx, scope.Organization, id, s.minInterval())
	if err != nil {
		return RunRequest{}, err
	}
	return RunRequest{ConnectorID: id, RunAt: at}, nil
}

func (s Service) validInterval(d time.Duration) bool {
	return d >= s.minInterval() && d <= 24*time.Hour
}

func (s Service) minInterval() time.Duration {
	if s.MinInterval <= 0 {
		return DefaultMinInterval
	}
	return s.MinInterval
}
