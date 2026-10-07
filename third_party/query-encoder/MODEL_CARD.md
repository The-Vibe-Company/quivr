# EmbeddingGemma 2: pinned model-card summary

Source: [Google DeepMind's model card at revision 914f7f89142e33e77833254d9c9b90c3cef7303b](https://huggingface.co/google/embeddinggemma-2/blob/914f7f89142e33e77833254d9c9b90c3cef7303b/README.md).

The upstream model maps text and other modalities into 768-dimensional vectors.
Its text component has 270 million parameters; vision and audio components can
be omitted when loading. Retrieval uses task-specific text prefixes and mean
pooling. The card declares Apache-2.0 and links Google's use policy.

This integration loads only the text component on CPU in float32, preserves the
configured query prefix, and verifies a locked snapshot before offline startup.
It does not enable local document routing or the model's other modalities.
The upstream dtype guidance permits float32 and bfloat16 and excludes float16.
Reported upstream capabilities do not establish parity, latency or data rights
for a particular deployment; measure those with the pinned assets and evaluation
queries before activation. See [NOTICE.md](NOTICE.md) for provenance and links.
