# Offline CPU text encoder provenance

The optional API image includes `google/embeddinggemma-2` by Google DeepMind at
revision `914f7f89142e33e77833254d9c9b90c3cef7303b`. The upstream model card declares
Apache-2.0 and links the [Google license](https://ai.google.dev/gemma/apache_2)
and [Gemma Prohibited Use Policy](https://ai.google.dev/gemma/prohibited_use_policy).
`LICENSE.apache-2.0` preserves the Apache-2.0 text for image redistribution; the
model repository has no standalone LICENSE file at this revision.

[MODEL_CARD.md](MODEL_CARD.md) summarizes the pinned upstream card.
`model-lock.json` records every included model asset's byte size and SHA-256.
`scripts/prepare_query_encoder.py` verifies these before atomic build-time
installation and again before offline startup. Weights are never committed.
The text path uses float32 without vision or audio encoders. Query parity and
production latency require separate deployment evidence.

`requirements.txt` hash-pins all runtime wheels for Python 3.12 on Linux x86_64,
including CPU-only PyTorch. Package metadata remains in the installed virtualenv;
this notice is not a complete transitive license or training-data rights audit.

Source: [pinned model card](https://huggingface.co/google/embeddinggemma-2/blob/914f7f89142e33e77833254d9c9b90c3cef7303b/README.md).
