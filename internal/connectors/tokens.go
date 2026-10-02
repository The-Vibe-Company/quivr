package connectors

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

const ActionConnectorAdmin = "connectors:admin"
const TokenRotationOverlap = 5 * time.Minute

var (
	ErrInvalidInstanceToken = publicerr.New("invalid_instance_token")
	ErrTokenInactive        = publicerr.New("token_inactive")
	ErrTokensUnavailable    = publicerr.New("tokens_unavailable")
)

// TokenInfo contains display metadata only, never a hash or bearer secret.
type TokenInfo struct {
	ID         string
	Prefix     string
	CreatedAt  time.Time
	RotatedAt  *time.Time
	RevokedAt  *time.Time
	ValidUntil *time.Time
}

// IssuedToken is returned only by an issuance, including a rotation. It must
// never be persisted or replayed; a lost response requires another issuance.
type IssuedToken struct {
	Token  TokenInfo
	Secret string
}
type TokenDeposit struct {
	ID, Prefix string
	Hash       []byte
}

// TokenStore durably owns token lifetimes. LoadInstanceTokenHash returns only
// a currently valid token on an enabled instance; it must not cache validity.
type TokenStore interface {
	CreateInstanceToken(context.Context, string, string, TokenDeposit) (TokenInfo, error)
	ListInstanceTokens(context.Context, string, string, string, int) ([]TokenInfo, error)
	RotateInstanceToken(context.Context, string, string, string, TokenDeposit) (TokenInfo, error)
	RevokeInstanceToken(context.Context, string, string, string) (TokenInfo, error)
	LoadInstanceTokenHash(context.Context, string, string, string) ([]byte, error)
}

func newInstanceToken() (IssuedToken, TokenDeposit, error) {
	var id [16]byte
	var secret [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return IssuedToken{}, TokenDeposit{}, err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return IssuedToken{}, TokenDeposit{}, err
	}
	identifier := hex.EncodeToString(id[:])
	raw := "qit_" + identifier + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(raw))
	deposit := TokenDeposit{ID: "token_" + identifier, Prefix: "qit_" + identifier[:8], Hash: hash[:]}
	return IssuedToken{Secret: raw}, deposit, nil
}

func (s Service) tokenInstance(ctx context.Context, scope corpus.Scope, id string, enabled bool) error {
	if !scope.Allows(ActionConnectorAdmin) {
		return corpus.ErrForbidden
	}
	instance, err := s.authorized(ctx, scope, id)
	if err != nil {
		return err
	}
	if enabled && !instance.Enabled {
		return ErrDisabled
	}
	if s.Tokens == nil {
		return ErrTokensUnavailable
	}
	return nil
}

func (s Service) CreateToken(ctx context.Context, scope corpus.Scope, id string) (IssuedToken, error) {
	if err := s.tokenInstance(ctx, scope, id, true); err != nil {
		return IssuedToken{}, err
	}
	issued, deposit, err := newInstanceToken()
	if err != nil {
		return IssuedToken{}, err
	}
	issued.Token, err = s.Tokens.CreateInstanceToken(ctx, scope.Organization, id, deposit)
	if err != nil {
		return IssuedToken{}, err
	}
	return issued, nil
}

func (s Service) ListTokens(ctx context.Context, scope corpus.Scope, id, after string, limit int) ([]TokenInfo, error) {
	if err := s.tokenInstance(ctx, scope, id, false); err != nil {
		return nil, err
	}
	// The transport may request one extra row to determine the next page.
	if limit < 1 || limit > 101 {
		return nil, ErrInvalid
	}
	return s.Tokens.ListInstanceTokens(ctx, scope.Organization, id, after, limit)
}

func (s Service) RotateToken(ctx context.Context, scope corpus.Scope, id, tokenID string) (IssuedToken, error) {
	if err := s.tokenInstance(ctx, scope, id, true); err != nil {
		return IssuedToken{}, err
	}
	issued, deposit, err := newInstanceToken()
	if err != nil {
		return IssuedToken{}, err
	}
	issued.Token, err = s.Tokens.RotateInstanceToken(ctx, scope.Organization, id, tokenID, deposit)
	if err != nil {
		return IssuedToken{}, err
	}
	return issued, nil
}

func (s Service) RevokeToken(ctx context.Context, scope corpus.Scope, id, tokenID string) (TokenInfo, error) {
	if err := s.tokenInstance(ctx, scope, id, false); err != nil {
		return TokenInfo{}, err
	}
	return s.Tokens.RevokeInstanceToken(ctx, scope.Organization, id, tokenID)
}

// AuthenticateToken checks durable validity for every call, then compares
// fixed-length digests in constant time. Revocation affects authentications
// after its commit; already admitted deliveries may finish.
func (s Service) AuthenticateToken(ctx context.Context, org, id, raw string) error {
	if len(raw) != 80 || !strings.HasPrefix(raw, "qit_") || raw[36] != '.' {
		return ErrInvalidInstanceToken
	}
	if _, err := hex.DecodeString(raw[4:36]); err != nil {
		return ErrInvalidInstanceToken
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(raw[37:]); err != nil || len(decoded) != 32 {
		return ErrInvalidInstanceToken
	}
	if s.Tokens == nil {
		return ErrTokensUnavailable
	}
	stored, err := s.Tokens.LoadInstanceTokenHash(ctx, org, id, "token_"+raw[4:36])
	if errors.Is(err, corpus.ErrNotFound) {
		return ErrInvalidInstanceToken
	}
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(raw))
	if subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
		return ErrInvalidInstanceToken
	}
	return nil
}
