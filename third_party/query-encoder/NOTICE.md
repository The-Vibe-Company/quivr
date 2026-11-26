# Offline CPU text encoder provenance

The optional API image and CPU embedding service default to the locked INT8
ONNX Runtime text export of `google/embeddinggemma-2` by Google DeepMind
(source revision `914f7f89142e33e77833254d9c9b90c3cef7303b`), distributed at
`onnx-community/embeddinggemma-2-ONNX` revision
`daa72c51243991dfcaf9f9137d2c573d8f7790c0`. Both repositories declare
Apache-2.0 and link the [Google license](https://ai.google.dev/gemma/apache_2)
and [Gemma Prohibited Use Policy](https://ai.google.dev/gemma/prohibited_use_policy).
`LICENSE.apache-2.0` preserves the Apache-2.0 text for image redistribution; the
upstream model repository has no standalone LICENSE file at the pinned revision.

[MODEL_CARD.md](MODEL_CARD.md) summarizes the pinned upstream card.
`model-lock.json` records every included INT8 ONNX model asset's byte size and
SHA-256 (with the exact same `tokenizer.json` checksum as the upstream PyTorch
snapshot). `model-lock-pytorch.json` preserves the legacy PyTorch FP32 asset
lock for optional fallback builds (`QUIVR_QUERY_ENCODER_BACKEND=pytorch`).
`scripts/prepare_query_encoder.py` verifies these before atomic build-time
installation and again before offline startup. Weights are never committed.
The text path omits vision and audio encoders. Query parity and production
latency require separate deployment evidence.

`requirements.txt` hash-pins the minimal offline ONNX Runtime wheels for Python
3.12 on Linux x86_64 (`onnxruntime`, `tokenizers`, `numpy`, `flatbuffers`,
`packaging`, `protobuf`), installed without PyTorch or network hub clients.
`requirements-pytorch.txt` preserves the legacy PyTorch FP32 wheel lock.
Package metadata remains in the installed virtualenv; this notice is not a
complete transitive license or training-data rights audit.

Sources:
- [pinned model card](https://huggingface.co/google/embeddinggemma-2/blob/914f7f89142e33e77833254d9c9b90c3cef7303b/README.md)
- [pinned ONNX export](https://huggingface.co/onnx-community/embeddinggemma-2-ONNX/tree/daa72c51243991dfcaf9f9137d2c573d8f7790c0)
