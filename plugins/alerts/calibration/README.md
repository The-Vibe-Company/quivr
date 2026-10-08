# Local-vector calibration

This reference records how per-space cosine threshold suggestions were measured for alert
authors and operators. All scores are measured, not classifier probabilities.

## Model and inputs

Measured on October 1, 2026 with `intfloat/multilingual-e5-small`, revision
`614241f622f53c4eeff9890bdc4f31cfecc418b3`, the revision in
[`core.ingest`'s profile](../../core-ingest/profile.json). No running ingestion
service was available, so the measurement used the model author's official
Transformers path: evaluation mode, float32 CPU inference, attention-mask mean
pooling and L2-normalized vectors. Cosine is their dot product.

The [script](calibrate_vectors.py) uses the same `query: <description>` and
`passage: <title>\n\n<body>` inputs as core.ingest. All articles fit one body
segment; maximum token counts were title 15, body 66, query 14 and full model
input 85, below the profile's limits of 64, 384, 256 and 512. There was no
truncation, window splitting or title-only segment to change the maximum score.
Long articles still need calibration with the core's real segmentation path.

Runtime versions: Python 3.12.14, PyTorch `2.14.1+cpu`, Transformers `5.18.0`,
Tokenizers `0.23.2`. The [model card](https://huggingface.co/intfloat/multilingual-e5-small)
documents the pooling and query/passage prefixes. The model download requires
network access; classification afterwards is local and needs no Jev key.

## Fixtures

The existing [neutral article set](set.json) contains 13 French and English
articles and six English descriptions. [French query translations](vector-queries.json)
add the reverse language direction without duplicating article text or labels.
Every article is compared with every description: 156 pairs, 16 labeled
positives and 140 negatives. The original labels remain unchanged, including
hard negatives that mention similar subjects.

[`vectors-results.json`](vectors-results.json) contains every exact prefixed
input, all 156 scores, model revision, runtime versions, token counts, measurement
time and a threshold sweep. Article indices are zero-based indices in `set.json`.

| Description | Article | Direction | Cosine |
| --- | --- | --- | --- |
| Labour strikes at ports and harbours | 0: Harbour staff walk out over pay | EN paraphrase | 0.886641 |
| Labour strikes at ports and harbours | 1: Les dockers de Portval votent la grève | EN → FR | 0.815391 |
| Grèves des travailleurs dans les ports | 0: Harbour staff walk out over pay | FR → EN | 0.862862 |
| Diplomatic tensions between two countries over visa rules | 4: Visa restrictions and a summoned ambassador | EN → FR | 0.806005 |
| Tensions diplomatiques entre deux pays à propos des règles de visa | 5: Estavia summons envoy as visa row deepens | FR → EN | 0.813364 |
| Labour strikes at ports and harbours | 9: Rovers sign striker for club-record fee | Unrelated control | 0.765448 |
| Grèves des travailleurs dans les ports | 9: Rovers sign striker for club-record fee | Unrelated control | 0.743865 |

## E5 suggestion and limitations

The E5 suggestion **0.80** is the highest hundredth that retains all 16 labeled
positives, including the cross-language examples. Positive scores range from
0.806005 to 0.893625; negatives range from 0.712355 to 0.860294. The ranges overlap,
so no single threshold classifies this whole set correctly.

| Threshold | True positives | False positives | False negatives |
| --- | --- | --- | --- |
| 0.80 | 16 | 11 | 0 |
| 0.81 | 15 | 7 | 1 |
| 0.82 | 13 | 6 | 3 |
| 0.83 | 12 | 4 | 4 |
| 0.87 | 5 | 0 | 11 |

At 0.80, accuracy is 92.95%, precision 59.26%, recall 100% and F1 0.7442.
These are in-sample measurements, not estimates of production quality. Among
the false positives, heatwave/drought against the English flood description
scores 0.860294; a railway strike against the French port-strike description
scores 0.852966; growing container traffic mentioning an old strike against
the English port description scores 0.833750. Several unrelated French-query
pairs also exceed 0.80. Raising the threshold loses cross-language positives;
use per-alert overrides or a keyword AND gate for a stricter topic constraint.
Do not use the Jev probability threshold, 0.5, as a cosine threshold.

## Reproduce the measurement

From `plugins/alerts`, in a Python 3.12+ environment:

```bash
pip install torch==2.14.1 --index-url https://download.pytorch.org/whl/cpu
pip install transformers==5.18.0 tokenizers==0.23.2
python3 calibration/calibrate_vectors.py --output /tmp/alerts-vectors-results.json
```

The run prints 156 pairs, 16 positives and 140 negatives, followed by the score
ranges. Small platform-dependent floating-point differences are possible.
This measurement stays outside unit tests and CI's quick lane. Recalibrate
before changing model weights, templates, segmentation or the default threshold.

## EmbeddingGemma 2 measurement

Measured on October 8, 2026 with `google/embeddinggemma-2`, revision
`914f7f89142e33e77833254d9c9b90c3cef7303b`, CPU float32, two threads and
batches of four inputs. The official SentenceTransformers text-only encoder
uses mean pooling, its learned 512-to-768 projection and L2 normalization.
The [pinned model card](https://huggingface.co/google/embeddinggemma-2/blob/914f7f89142e33e77833254d9c9b90c3cef7303b/README.md)
documents the pipeline. No paid embedding service was called.

Queries use `task: search result | query: <description>`; documents use
`title: <title> | text: <body>`, matching the hosted embedding plugin's Gemma
search templates. The same 13 articles and 12 descriptions produce 156 pairs.
Maximum token counts were title 14, body 61, query 13 and full input 82.
Every document fits one body passage; no truncation or segmentation occurred.
Separate title-only passages and long-document segmentation remain unmeasured.

[`vectors-gemma-results.json`](vectors-gemma-results.json) records exact inputs,
all scores, revision, dimensions, runtime versions and the threshold sweep.
Positive scores range from 0.659968 to 0.831484; negatives from 0.472581 to
0.728274. They overlap, so no single threshold perfectly separates this set.

| Threshold | True positives | False positives | False negatives |
| --- | --- | --- | --- |
| 0.65 | 16 | 6 | 0 |
| 0.66 | 15 | 5 | 1 |
| 0.70 | 14 | 2 | 2 |
| 0.80 | 4 | 0 | 12 |

**0.65** is the highest hundredth retaining all labeled positives. This is
in-sample advice for this revision, 768 dimensions and these templates, not
production-quality evidence. The plugin has no implicit threshold: configure
`vectors.thresholds[<vector-space-id>]` on its pin, or `threshold` on a
Subscription. The earlier global `vectors.threshold` setting is replaced by
the per-space map. Recalibrate after changing weights, dimensions or inputs.

To reproduce the bounded CPU run from `plugins/alerts`, in Python 3.12+:

```bash
pip install torch==2.8.0+cpu torchvision==0.23.0+cpu \
  --index-url https://download.pytorch.org/whl/cpu
pip install sentence-transformers==6.1.0 transformers==5.19.0 pillow==12.3.0
python3 calibration/calibrate_vectors.py --model gemma --threads 2 \
  --output /tmp/alerts-vectors-gemma-results.json
```

The output path must be new; the script refuses to overwrite evidence. Model
weights download from the pinned public revision, then inference stays on CPU.
Torchvision is required by the Transformers processor import even with the
vision and audio encoders disabled. Runtime versions were PyTorch `2.8.0+cpu`,
Transformers `5.19.0`, SentenceTransformers `6.1.0` and Tokenizers `0.23.2`.
