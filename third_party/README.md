# Third-party notices and dependency inventory

This index covers what `make verify` builds and runs. It is an evaluation-stage
notice list, not a legal review, and it certifies nothing for production.

| Artifact | Where it comes from | Notice |
| --- | --- | --- |
| `quivr` binary (Go modules linked into it) | `go.mod` / `go.sum` | Listed per build in `dependency-inventory.json` (`go version -m`), with the licence read from each module's licence file |
| E5 model `intfloat/multilingual-e5-small`, revision `614241f6…` | Downloaded and SHA-256-checked by `scripts/prepare_embeddings.py`; never committed | [e5/NOTICE.md](e5/NOTICE.md), [e5/MODEL_CARD.md](e5/MODEL_CARD.md) |
| Text Embeddings Inference (TEI) image | Pinned by digest in `deploy/compose/compose.yaml` | [e5/LICENSE.tei](e5/LICENSE.tei) |
| Hugging Face Tokenizers 0.23.2 | Hash-pinned wheel, `tokenizer/requirements-linux-x86_64.txt` | [tokenizer/NOTICE.md](tokenizer/NOTICE.md), [tokenizer/LICENSE.tokenizers](tokenizer/LICENSE.tokenizers) |
| PostgreSQL, Temporal, SeaweedFS and Weaviate images | Pinned by digest in `deploy/compose/compose.yaml`; run, not redistributed | Upstream images; licences not inventoried here |
| OpenAPI Generator image | Pinned by digest in `scripts/contracts.sh`; contract checks only | Upstream image; licence not inventoried here |
| `quivr-search` demo UI packages | `quivr-search/package-lock.json` | Listed in `dependency-inventory.json` with the licence the lock file records |

`dependency-inventory.json` is written by `python3 scripts/inventory.py <quivr-binary> <output>`,
and each `make verify` run writes one next to its report. When a licence cannot
be identified from a licence file or a lock entry, the inventory says
`unclassified` instead of guessing.

## Unresolved

- **Model provenance.** The E5 training data and the declared MIT licence are
  as published on the upstream model card. They are not independently verified,
  and the model repository has no standalone LICENSE file at the pinned revision.
- **Platforms.** Only linux/amd64 is supported and tested. macOS and linux/arm64
  have no pinned tokenizer wheel or TEI digest, and the harness's process checks
  read `/proc`. No other platform is claimed.
- **Container images.** Their licences are not inventoried; they are used as
  pinned upstream images.
