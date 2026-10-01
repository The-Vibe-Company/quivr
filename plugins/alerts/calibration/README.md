# Local-vector calibration

This reference records how the default cosine threshold was chosen for alert
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

## Default and limitations

The default **0.80** is the highest hundredth that retains all 16 labeled
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
