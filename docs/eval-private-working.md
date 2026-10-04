# Explore a private encrypted working set

You can mix public development sets with a private encrypted working set. The
operator supplies a TREC archive encrypted with age, its ciphertext SHA-256 and
its TREC fingerprint (from `trec.fingerprint` before encryption). Use separate
age recipients for working and held-out artifacts. Tier 1 accepts only a
`working` descriptor; its working identity must not decrypt held-out ciphertext.
The [private-set builder](agents/news-set.md) enforces separate recipients.

Add an opaque set name under `sets` in the policy. This example was validated
locally with placeholder digests; replace both with your artifact's actual values:

```json
"private-example": {
  "split": "dev",
  "input": {
    "name": "private-example",
    "version": "v1",
    "split": "working",
    "digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "fingerprint": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "privacy": "private"
  }
}
```

Runtime locations and keys stay outside that policy. On the dispatch machine,
set `EVAL_WORKING_RUNTIME` to a JSON object mapping each private name to
`volume`, `artifact`, `secret`, `identity_env` and `provider_consent`. The consent
must be `true`: hosted embeddings and reranking send content to the configured
providers. Keep runtime configuration in an ignored operator file or secret
manager. The same environment is needed on campaign start and resume.

Use the Modal account/environment that owns these resources. Authenticate as
described in the [Modal comparison prerequisites](eval-modal.md#prerequisites).
Launch attaches the named Secret and mounts the named Volume automatically.
For example, requiring Modal operator credentials and not run live:

```sh
modal volume create eval-working-example
modal volume put eval-working-example /secure/working.tar.gz.age working.tar.gz.age
modal secret create eval-working-example-secret \
  EVAL_WORKING_AGE_EXAMPLE="$(cat /secure/working-identity.txt)"
export EVAL_WORKING_RUNTIME='{
  "private-example": {
    "volume": "eval-working-example",
    "artifact": "working.tar.gz.age",
    "secret": "eval-working-example-secret",
    "identity_env": "EVAL_WORKING_AGE_EXAMPLE",
    "provider_consent": true
  }
}'
```

The artifact must be a single filename ending in `.tar.gz.age`. Volume and
Secret names are opaque identifiers. Each Secret supplies a distinct environment
name matching `EVAL_WORKING_AGE_[A-Z0-9_]+`, containing only its working age
identity. Upload only working ciphertext to this private Volume; grant runner
access to that Volume and Secret, never the held-out key or Volume. Never add
these resources to the public cache Volume. Retain ciphertext and the Secret
until the campaign is archived; then delete them through your operator tooling.

Dry-run policy validation needs no runtime configuration or keys. Live launch
mounts each working Volume at `/eval-working/<set name>`, checks ciphertext and
content fingerprints, and decrypts into a temporary directory inside the runner.
A mismatch or missing/wrong identity fails before provider calls. Plaintext and
vectors are removed when the invocation ends; private vectors are kept in memory
and never enter the shared embedding cache or text-derived SQL cache claims.

Both sides of a private comparison run inside one invocation, so choose a
`max_seconds` that fits both under your caps. Private baselines are measured
again for each candidate. Measurement evidence returned to the dispatcher contains only aggregate scores,
paired query count/delta/p-value and a latency-comparability boolean. SQL, public MLflow and the
outbox store no private per-query scores or latency IDs. Private failures log the
exception class and a fixed message. Completed comparisons replay aggregate
records without decryption or provider calls. Start/resume still needs the runtime
references and access to the configured Modal resources. Public and private sets share the
same family-wide significance correction and four gates.

## Next

- [Run the Modal comparison](eval-modal.md).
- [Run a search campaign](search-campaigns.md).
