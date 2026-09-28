# Local E5 / TEI reference

THE-646 uses the accepted Linux x86_64 prototype pins, not a newly selected model.
`model-lock.json` records the seven snapshot file hashes and the TEI OCI digest.
`MODEL_CARD.md` archives the model card at revision
`614241f622f53c4eeff9890bdc4f31cfecc418b3`, which declares MIT.
The model repository has no standalone LICENSE at that revision (the earlier
pinned lookup returned 404); this notice does not invent one.
`LICENSE.tei` preserves the Apache-2.0 text from TEI source commit
`06670157fb6c1523482219bdb2d1660277d38088` (v1.9.3).

Sources:
- https://huggingface.co/intfloat/multilingual-e5-small/tree/614241f622f53c4eeff9890bdc4f31cfecc418b3
- https://github.com/huggingface/text-embeddings-inference/tree/06670157fb6c1523482219bdb2d1660277d38088
- Accepted segmentation/embedding research: commit `9e59d3bf12afe5d20ce1b0afd5775e464b2ebddf`, `research/text-segmentation-embedding-profile.md`.

`python3 scripts/prepare_embeddings.py` downloads absent/mismatched files, checks
SHA-256 before atomic replacement and reports downloaded bytes/preparation time.
No weights or image layers are committed here. Subsequent runtime execution is
offline: a read-only model mount, `HF_HUB_OFFLINE=1`, an internal Docker network,
float32, mean pooling and explicit `--auto-truncate false`. The adapter also checks
TEI's version, source SHA, dtype, pooling, sequence limit and truncation setting.
TEI's HTTP `/info` cannot attest mounted weight hashes; the local initializer's
file verification and pinned container are the trust boundary.

`internal/adapters/tei/space.json` is canonical JSON using only ASCII keys/strings,
bounded integers and booleans. Its domain-separated SHA defines the Vector Space.
Document Artifacts include the producer image/backend/architecture and client
source digest; query vectors remain ephemeral. Each logical call has one input
(within the accepted 32-input/8192-token ceiling). TEI may batch concurrent calls;
only the tested pinned Linux CPU backend is currently supported, without a claim
of bitwise compatibility with other runtimes or architectures.

Warm preparation on the development host verified all files in 0.497 s with zero
bytes downloaded. One idle TEI process used approximately 1.01 GiB RAM in Docker;
the cached model files require about 950 MB, in addition to images and data volumes.
These are observations, not peak resource guarantees. Start with at least 4 CPU
threads, 4 GiB available RAM and several GiB free disk for the full local stack;
CI and full-run reports provide execution evidence. Fresh-cache CI exercises
preparation; production load/latency measurement belongs to THE-661.

The model card and OCI pin establish declared provenance. They do not constitute
a training-data rights review or a complete transitive image license/SBOM audit.
The broader image redistribution/production packaging gate from the research
remains separate from this local evaluation implementation.

The FR/EN fixture in `internal/adapters/weaviate/testdata/relevance-v1.json` was
originally written for the accepted research and released under CC0-1.0. It
contains no customer or other third-party article text. Its dedicated collection isolates
BM25 statistics from other test documents. Results are reported by mode;
THE-641 tracks the known hybrid deficit. Structured ingestion remains THE-648.
