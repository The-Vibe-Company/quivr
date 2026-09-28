package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var (
	ErrConflict          = errors.New("idempotency_conflict")
	ErrNamespaceInUse    = errors.New("source_namespace_in_use")
	ErrUnsupportedKind   = errors.New("unsupported_connector_kind")
	ErrInvalidConfig     = errors.New("invalid_config")
	ErrInvalidCredential = errors.New("invalid_credential")
	ErrInvalidInterval   = errors.New("invalid_interval")
	ErrInvalid           = errors.New("invalid_input")
	ErrDisabled          = errors.New("connector_disabled")
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
}

// Service authorizes and validates Connector Instance commands.
type Service struct {
	Store       Store
	Registry    *Registry
	Sealer      Sealer
	MinInterval time.Duration
}

// CreateInput is a creation command. Secret is the raw kind-specific secret.
type CreateInput struct {
	Key                      string          `json:"idempotency_key"`
	CorpusID                 string          `json:"corpus_id"`
	Namespace                string          `json:"source_namespace"`
	Kind                     string          `json:"kind"`
	Config                   json.RawMessage `json:"config"`
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
func (s Service) Create(ctx context.Context, scope corpus.Scope, in CreateInput) (Instance, error) {
	if !scope.Allows("connectors:write") {
		return Instance{}, corpus.ErrForbidden
	}
	if !scope.Contains(in.CorpusID) {
		return Instance{}, corpus.ErrNotFound
	}
	if !validText(in.Key) || !validText(in.Namespace) || !validText(in.CorpusID) {
		return Instance{}, ErrInvalid
	}
	connector, ok := s.Registry.Lookup(in.Kind)
	if !ok {
		return Instance{}, ErrUnsupportedKind
	}
	if err := s.Registry.validate(in.Kind, in.Config, in.Secret); err != nil {
		return Instance{}, err
	}
	if cc, ok := connector.(ConfigChecker); ok && cc.CheckConfig(in.Config, time.Now()) != nil {
		return Instance{}, ErrInvalidConfig
	}
	interval := connector.DefaultInterval()
	if in.IntervalSeconds != nil {
		interval = time.Duration(*in.IntervalSeconds) * time.Second
	}
	if interval < s.minInterval() || interval > 24*time.Hour {
		return Instance{}, ErrInvalidInterval
	}
	silent, warning := DefaultSilentAfter, DefaultCredentialWarning
	if in.SilentAfterSeconds != nil {
		silent = time.Duration(*in.SilentAfterSeconds) * time.Second
	}
	if in.CredentialWarningSeconds != nil {
		warning = time.Duration(*in.CredentialWarningSeconds) * time.Second
	}
	if silent <= 0 || warning < 0 {
		return Instance{}, ErrInvalid
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return Instance{}, ErrInvalid
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Instance{}, err
	}
	n := NewInstance{Instance: Instance{Organization: scope.Organization, ID: "connector_" + hex.EncodeToString(id[:]), CorpusID: in.CorpusID, Namespace: in.Namespace, Kind: in.Kind, Config: in.Config, Interval: interval, SilentAfter: silent, CredentialWarning: warning, Enabled: true}, RequestKey: in.Key, RequestDigest: s.Sealer.Digest("create", canonical)}
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
	if !scope.Allows("connectors:read") {
		return Instance{}, corpus.ErrForbidden
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
func (s Service) List(ctx context.Context, scope corpus.Scope, corpusID, after string, limit int) ([]Instance, error) {
	if !scope.Allows("connectors:read") {
		return nil, corpus.ErrForbidden
	}
	if corpusID != "" && !scope.Contains(corpusID) {
		return []Instance{}, nil
	}
	return s.Store.ListConnectors(ctx, scope, corpusID, after, limit)
}

// Disable stops scheduling; it is idempotent and absorbing.
func (s Service) Disable(ctx context.Context, scope corpus.Scope, id string) (Instance, error) {
	if !scope.Allows("connectors:write") {
		return Instance{}, corpus.ErrForbidden
	}
	if _, err := s.authorized(ctx, scope, id); err != nil {
		return Instance{}, err
	}
	return s.Store.DisableConnector(ctx, scope.Organization, id)
}

// ReplaceCredential deposits a new credential version.
func (s Service) ReplaceCredential(ctx context.Context, scope corpus.Scope, id string, in CredentialInput) (Instance, error) {
	if !scope.Allows("connectors:write") {
		return Instance{}, corpus.ErrForbidden
	}
	inst, err := s.authorized(ctx, scope, id)
	if err != nil {
		return Instance{}, err
	}
	if !validText(in.Key) {
		return Instance{}, ErrInvalid
	}
	// A disabled instance is refused by the store after replay detection, so a
	// retried rotation that already succeeded still replays.
	if in.Secret == nil {
		return Instance{}, ErrInvalidCredential
	}
	if err = s.Registry.validate(inst.Kind, inst.Config, in.Secret); err != nil {
		return Instance{}, err
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return Instance{}, ErrInvalid
	}
	sealed, err := s.Sealer.Seal(scope.Organization, id, in.Secret)
	if err != nil {
		return Instance{}, err
	}
	sealed.ExpiresAt = in.ExpiresAt
	return s.Store.ReplaceCredential(ctx, scope.Organization, id, CredentialDeposit{RequestKey: in.Key, RequestDigest: s.Sealer.Digest("credential", canonical), Sealed: sealed})
}

func (s Service) minInterval() time.Duration {
	if s.MinInterval <= 0 {
		return DefaultMinInterval
	}
	return s.MinInterval
}
