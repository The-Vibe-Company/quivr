package content

import (
	"context"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TrustedRecord reads within an Organization established by durable worker
// input. HTTP callers must use Record with their authenticated Scope instead.
func (s Service) TrustedRecord(ctx context.Context, org, id string) (Record, error) {
	return s.record(ctx, corpus.Scope{Organization: org, Corpora: []string{"*"}}, id)
}

// TrustedVersion reads canonical content within the Organization and Corpus
// established by durable work. Relations retain that same Corpus visibility.
func (s Service) TrustedVersion(ctx context.Context, org, corpusID, recordID, id string) (Version, error) {
	return s.version(ctx, corpus.Scope{Organization: org, Corpora: []string{corpusID}}, recordID, id)
}

// TrustedAccept ingests from an already authorized connector target. It keeps
// validation and idempotency shared with Accept, without manufacturing grants.
func (s Service) TrustedAccept(ctx context.Context, org, corpusID string, c Command) (Receipt, error) {
	return s.accept(ctx, corpus.Scope{Organization: org, Corpora: []string{corpusID}}, c, true)
}

// TrustedWithdraw fences an identity from the connector's own Corpus.
func (s Service) TrustedWithdraw(ctx context.Context, org, corpusID string, w Withdrawal) (Receipt, error) {
	return s.withdraw(ctx, corpus.Scope{Organization: org, Corpora: []string{corpusID}}, w, true)
}
