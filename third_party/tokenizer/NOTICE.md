# Pinned tokenizer provenance

THE-645 uses Hugging Face **Tokenizers 0.23.2**. Its Apache-2.0 license is
preserved in [LICENSE.tokenizers](LICENSE.tokenizers), retrieved from the
[upstream tag](https://github.com/huggingface/tokenizers/blob/v0.23.2/LICENSE).
The supported local/CI target is Linux x86_64, CPython >=3.10; the selected
manylinux ABI3 wheel is pinned by SHA-256 in
[requirements-linux-x86_64.txt](requirements-linux-x86_64.txt). `make dev` on
macOS arm64 installs the same release's macOS 11 arm64 ABI3 wheel, pinned in
[requirements-macos-arm64.txt](requirements-macos-arm64.txt). Optional Hub
packages are not installed: runtime loading uses an already verified local file.
The selected wheel contains no standalone LICENSE or NOTICE files; the upstream
Apache-2.0 text is therefore preserved explicitly alongside this notice.

The tokenizer is downloaded from **intfloat/multilingual-e5-small**, revision
`614241f622f53c4eeff9890bdc4f31cfecc418b3`, with SHA-256
`0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39`.
The [pinned model card](https://huggingface.co/intfloat/multilingual-e5-small/blob/614241f622f53c4eeff9890bdc4f31cfecc418b3/README.md)
declares `license: mit`; that snapshot does not provide a standalone `LICENSE`
file (the checked URL returned 404). No model weights or tokenizer payload are
committed or redistributed in this repository. Preparation fetches the tokenizer
from the pinned upstream URL and verifies its digest before use. This records
the upstream declaration, without claiming to audit model training-data rights.

The [profile](../../plugins/core-ingest/profile.json) records model identity,
implementation version, token budgets, templates, source policy and technical
limits. Each persisted Segmentation stores this profile and the SHA-256 of the
Go recipe source. The profile also pins the executable Python helper SHA-256;
the adapter verifies it and executes the verified bytes. The same typed profile
drives the Go budgets and the persisted provenance. Segment metadata stores original and UTF-8 coordinates,
source/input checksums, token intervals, overlap and forced-boundary flags, full
versus capped-title provenance and the exact model input. No vectors or model
inference are produced in this ticket. THE-646 owns embedding artifacts.

Sources and experiment: the accepted
[THE-553 research](https://github.com/The-Vibe-Company/quivr/blob/9e59d3bf12afe5d20ce1b0afd5775e464b2ebddf/research/text-segmentation-embedding-profile.md),
[pinned Tokenizers implementation](https://github.com/huggingface/tokenizers/tree/v0.23.2)
and the [offset API](https://huggingface.co/docs/tokenizers/en/api/encoding).
Reproduce with `python3 scripts/prepare_tokenizer.py` followed by `make verify`.
No hosted model key is needed; after preparation the tokenizer runs offline.
