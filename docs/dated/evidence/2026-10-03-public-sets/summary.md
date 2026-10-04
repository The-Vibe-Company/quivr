# Public search sets and local baseline

Date: 2026-10-03
Status: evidence

The suite contains ten default sets (five French) and three opt-in restricted diagnostics.
Default sets permit commercial-development benchmarking and are the only promotion-eligible
sets. Restricted sources are downloaded at run time only; no source documents, query text,
judgments or per-query results from those sets are redistributed here.
All sources are pinned by content SHA-256 and immutable revisions in
[`public_sets.json`](../../../../scripts/eval/public_sets.json).

Each linked report has JSON and Markdown versions with source/sample counts, judgments per
query, relevance grades, query length distributions, language, query-type mix, licence evidence,
known issues and a sample fingerprint. Query-type counts use an explicit lexical question/statement
heuristic; manual intent labels and judge agreement are unavailable. Sampling retains all available
judgments whose documents fit the engine limit, including explicit zero grades. Queries whose
positive documents are absent or too long are excluded; dropped nonpositive judgments are counted.

## Local measurements

One complete direct run of the pinned `intfloat/multilingual-e5-small` revision
`614241f622f53c4eeff9890bdc4f31cfecc418b3` was performed on every set, on CPU. These are exact
cosine comparisons with the best document piece, top 10, 1,800-character e5 windows and 200-character
overlap. Each JSON report preserves the three mean metrics, dimensions, piece count, timings,
run timestamp/settings and the original direct-run SHA-256. Results measure the sampled direct
retrieval setup rather than full-corpus retrieval or the running engine.
The existing MIRACL-fr, MLDR-fr and SciFact sample fingerprints remain unchanged.
No paid provider was called by the worker.

| Set / quality report | Tier | Language | Documents | Queries | Mean judged depth | E5 nDCG@10 | E5 perfect share | Hosted cap USD |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| [miracl-fr](miracl-fr.md) ([JSON](miracl-fr.json)) | default | fr | 5000 | 343 | 8.68 | 0.7116 | 23.0% | 0.66 |
| [mldr-fr](mldr-fr.md) ([JSON](mldr-fr.json)) | default | fr | 600 | 200 | 1.00 | 0.9167 | 87.0% | 2.27 |
| [scifact](scifact.md) ([JSON](scifact.json)) | default | en | 2000 | 300 | 1.13 | 0.7526 | 61.7% | 0.75 |
| [xpqa-fr](xpqa-fr.md) ([JSON](xpqa-fr.json)) | default | fr | 1548 | 200 | 2.05 | 0.5553 | 29.5% | 0.04 |
| [webfaq-fr](webfaq-fr.md) ([JSON](webfaq-fr.json)) | default | fr | 2500 | 200 | 1.00 | 0.9254 | 86.0% | 0.23 |
| [mkqa-fr](mkqa-fr.md) ([JSON](mkqa-fr.json)) | default | fr | 2000 | 200 | 1.50 | 0.0966 | 4.0% | 0.02 |
| [fiqa](fiqa.md) ([JSON](fiqa.json)) | default | en | 2000 | 200 | 2.65 | 0.6421 | 28.5% | 0.43 |
| [trec-covid](trec-covid.md) ([JSON](trec-covid.json)) | default | en | 10000 | 5 | 1260.20 | 0.8629 | 40.0% | 3.08 |
| [arguana](arguana.md) ([JSON](arguana.json)) | default | en | 2000 | 200 | 1.00 | 0.5703 | 31.5% | 0.57 |
| [scidocs](scidocs.md) ([JSON](scidocs.json)) | default | en | 6000 | 200 | 29.94 | 0.2425 | 1.0% | 1.77 |
| [alloprof](alloprof.md) ([JSON](alloprof.json)) | restricted | fr | 2000 | 200 | 1.00 | 0.3244 | 18.5% | 1.80 |
| [bsard](bsard.md) ([JSON](bsard.json)) | restricted | fr | 2000 | 200 | 1.00 | 0.3112 | 17.0% | 0.47 |
| [nfcorpus](nfcorpus.md) ([JSON](nfcorpus.json)) | restricted | en | 3600 | 200 | 41.20 | 0.3167 | 5.5% | 1.39 |

E5-perfect queries are those with nDCG@10 exactly 1. Joint saturation requires both e5 and
Cohere Pro to reach exactly 1 on the same query. It is explicitly unavailable in every report
until the coordinator runs the hosted baseline. It is never inferred from e5 scores alone.
Sparse one-positive judgments and sampled distractors can make a dataset appear saturated.
TREC-COVID retains deep graded judgments but only five seeded topics, which cannot establish
significance alone. MKQA is short-answer retrieval on its available train split, a limited proxy
for passage retrieval. WebFAQ is sampled FAQ retrieval and may include duplicate answers.

## Licence decisions and French replacements

Syntec is excluded: its [MTEB card](https://huggingface.co/datasets/mteb/SyntecRetrieval)
says unknown licence, and the original card does not resolve it. mMARCO is excluded: its
[translation card](https://huggingface.co/datasets/unicamp-dl/mmarco) says Apache-2.0, but inherited
[MS MARCO terms](https://microsoft.github.io/msmarco/) remain unreconciled.
Alloprof and BSARD use their MTEB noncommercial licences; NFCorpus uses its original
[academic-only terms](https://www.cl.uni-heidelberg.de/statnlpgroup/nfcorpus/).
These three restricted sets are never promotion evidence. The original Alloprof card's MIT
metadata conflicts with the MTEB card, so the restrictive licence controls this registry.
Per-set reports and the registry record the checked licences and pinned card links.

[WebFAQ](https://huggingface.co/datasets/mteb/WebFAQRetrieval) is CC BY 4.0;
[MKQA](https://huggingface.co/datasets/mteb/MKQARetrieval) is CC BY 3.0. Their French retrieval
splits bring the default suite to five French sets without adding a duplicate MIRACL sample.
PIAF, mFAQ and Belebele conversions were not added after the requested French count was met.
[XQuAD's published retrieval conversion](https://huggingface.co/datasets/mteb/XQuADRetrieval)
has no French split.

## Coordinator hosted hand-off

[`hosted-plan.json`](hosted-plan.json) records exact argument arrays for each set, its fingerprint,
byte-based input estimate and per-set token/USD caps. A single hosted pass is estimated at
$4.8811 for the default suite and $6.7056 including restricted diagnostics. The sum of the rounded
per-set caps is **$9.82 default**, or **$13.48 including restricted diagnostics**. These caps cover
roughly twice the estimated input and remain below $20. Estimates use the existing recorded
Cohere Pro rate of $0.12/million input tokens. If the provider rate changed, recalculate
the estimates and per-set USD caps at the new rate, keeping the total below $20, and append
`--price Cohere-Embed-V5-Pro=<USD_PER_MILLION>` to each direct command before running.
The direct runner enforces its token and USD caps
before each batch and includes retry accounting. Each invocation must finish successfully;
a failed/capped run is not quality evidence.

Run locally with the [direct evaluation dependencies](../../../agents/evaluation.md), in the
coordinator environment providing hosted credentials. This command executes only the ten
default sets, then computes joint saturation from each completed direct JSON (the direct
runner also runs e5 as its reference):

```sh
python3 - <<'PYTHON'
import json
import subprocess
from pathlib import Path
plan = json.loads(Path('docs/dated/evidence/2026-10-03-public-sets/hosted-plan.json').read_text())
for item in plan['sets']:
    if item['tier'] != 'default':
        continue
    subprocess.run(item['direct_command'], check=True)
    subprocess.run(item['saturation_command'], check=True)
PYTHON
```

To run only the restricted diagnostics under their permitted uses, change the tier filter to
`if item['tier'] != 'restricted':`. The saved commands explicitly include `--include-restricted`
and label their output as ineligible for promotion. Running both tiers sums to the $13.48 cap.
The quality command rejects wrong fingerprints, incomplete runs or missing sampled query scores.
Output goes under `.scratch/eval/cohere/` and `.scratch/eval/cohere-quality/`; it refuses overwrites.
Publish hosted aggregates and joint saturation in a new dated evidence folder after the run,
keeping restricted data and per-query payloads out of the public repository.

To reproduce a local baseline without hosted calls:

```sh
python3 scripts/eval/direct_bakeoff.py --set webfaq-fr \
  --models 'multilingual-e5-small (current)' --max-input-tokens 1000 --max-usd 1 \
  --out .scratch/eval/local/webfaq-fr.json
python3 scripts/eval/quality_reports.py --set webfaq-fr \
  --e5-run .scratch/eval/local/webfaq-fr.json --out .scratch/eval/local-quality
```

The hosted caps do not limit local e5 compute. Scientific measurements run locally, never in
GitHub CI. CI uses tiny offline fixtures and an offline registry preview.
