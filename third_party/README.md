# Third-party notices and dependency inventory

This index covers what `make verify` builds and runs. It is an evaluation-stage
notice list, not a legal review, and it certifies nothing for production.

| Artifact | Where it comes from | Notice |
| --- | --- | --- |
| `quivr` binary (Go modules linked into it) | `go.mod` / `go.sum` | Listed per build in `dependency-inventory.json` (`go version -m`), with the licence read from each module's licence file |
| French light stemmer linked into `quivr` | Go adaptation of Apache Lucene FrenchLightStemmer (UniNE algorithm) | [french-light/NOTICE.md](french-light/NOTICE.md), [french-light/LICENSE.apache-2.0](french-light/LICENSE.apache-2.0) |
| E5 model `intfloat/multilingual-e5-small`, revision `614241f6…` | Downloaded and SHA-256-checked by `scripts/prepare_embeddings.py`; never committed | [e5/NOTICE.md](e5/NOTICE.md), [e5/MODEL_CARD.md](e5/MODEL_CARD.md) |
| Optional CPU text model `google/embeddinggemma-2`, revision `914f7f89…` and Python runtime | Build-time SHA-256-checked assets and hash-pinned Linux x86_64 wheels in `query-encoder/`; never downloaded at runtime | [query-encoder/NOTICE.md](query-encoder/NOTICE.md), [query-encoder/MODEL_CARD.md](query-encoder/MODEL_CARD.md), [query-encoder/LICENSE.apache-2.0](query-encoder/LICENSE.apache-2.0) |
| Text Embeddings Inference (TEI) image | Pinned by digest in `deploy/compose/compose.yaml` | [e5/LICENSE.tei](e5/LICENSE.tei) |
| Hugging Face Tokenizers 0.23.2 | Hash-pinned wheels, `tokenizer/requirements-linux-x86_64.txt` and `tokenizer/requirements-macos-arm64.txt` (one per supported host) | [tokenizer/NOTICE.md](tokenizer/NOTICE.md), [tokenizer/LICENSE.tokenizers](tokenizer/LICENSE.tokenizers) |
| PostgreSQL, Temporal, SeaweedFS and Weaviate images | Pinned by digest in `deploy/compose/compose.yaml`; run, not redistributed | Upstream images; licences not inventoried here |
| OpenAPI Generator image | Pinned by digest in `scripts/contracts.sh`; contract checks only | Upstream image; licence not inventoried here |
| pypdf 6.19.0 and cryptography 50.0.1 (reference plugin `plugins/pdf-text`) | Pinned in `plugins/pdf-text/pyproject.toml`, installed from PyPI into the plugin's virtualenv; they run in the plugin process and are never linked into `quivr` | pypdf BSD-3-Clause; cryptography Apache-2.0 OR BSD-3-Clause; see [plugins/pdf-text/README.md](../plugins/pdf-text/README.md#dependencies-and-licences) |
| `quivr-search` demo UI packages | `quivr-search/package-lock.json` | Listed in `dependency-inventory.json` with the licence the lock file records |

`dependency-inventory.json` is written by `python3 scripts/inventory.py <quivr-binary> <output>`,
and each `make verify` run writes one next to its report. When a licence cannot
be identified from a licence file or a lock entry, the inventory says
`unclassified` instead of guessing.

## Unresolved

- **Model provenance.** The E5 training data and the declared MIT licence are
  as published on the upstream model card. They are not independently verified,
  and the model repository has no standalone LICENSE file at the pinned revision.
- **Platforms.** linux/amd64 is supported and tested in CI. `make dev` also runs
  on macOS arm64, with its own pinned tokenizer wheel and the same amd64 TEI image
  under Docker Desktop's emulation. linux/arm64 has no pinned tokenizer wheel or
  TEI digest. No other platform is claimed.
- **Container images.** Their licences are not inventoried; they are used as
  pinned upstream images.
