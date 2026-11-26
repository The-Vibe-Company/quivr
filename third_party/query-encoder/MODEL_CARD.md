# EmbeddingGemma 2: pinned model-card summary

Source: [Google DeepMind's model card at revision 914f7f89142e33e77833254d9c9b90c3cef7303b](https://huggingface.co/google/embeddinggemma-2/blob/914f7f89142e33e77833254d9c9b90c3cef7303b/README.md).

The upstream model maps text and other modalities into 768-dimensional vectors.
Its text component has 270 million parameters; vision and audio components can
be omitted when loading. Retrieval uses task-specific text prefixes and mean
pooling. The card declares Apache-2.0 and links Google's use policy.

This integration defaults to the locked INT8 weight-quantized ONNX Runtime text
graph (`onnx-community/embeddinggemma-2-ONNX@daa72c51243991dfcaf9f9137d2c573d8f7790c0`,
`onnx/model_quantized.onnx`), preserves the configured query prefix and exact
`tokenizer.json`, and verifies the complete locked snapshot before offline
startup. An optional PyTorch FP32 fallback (`QUIVR_QUERY_ENCODER_BACKEND=pytorch`)
remains available using `model-lock-pytorch.json`. It does not enable local
document routing or the model's other modalities.
Reported upstream capabilities do not establish parity, latency or data rights
for a particular deployment; measure those with the pinned assets and evaluation
queries before activation. See [NOTICE.md](NOTICE.md) for provenance and links.
