"""Measure local E5 cosine scores; opt-in, outside the unit-test lane."""
from __future__ import annotations

import argparse
import importlib.metadata
import json
from datetime import datetime, timezone
from pathlib import Path

MODEL = "intfloat/multilingual-e5-small"
REVISION = "614241f622f53c4eeff9890bdc4f31cfecc418b3"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--threads", type=int, default=2)
    args = parser.parse_args()

    import torch
    import torch.nn.functional as functional
    from transformers import AutoModel, AutoTokenizer

    torch.set_num_threads(args.threads)
    fixtures = json.loads(Path(__file__).with_name("set.json").read_text())
    extra_queries = json.loads(Path(__file__).with_name("vector-queries.json").read_text())
    descriptions = {**fixtures["descriptions"], **{name: query["description"] for name, query in extra_queries.items()}}
    queries = ["query: " + text.strip().replace("\r\n", "\n").replace("\r", "\n") for text in descriptions.values()]
    passages = ["passage: " + article["title"].strip() + "\n\n" + article["text"].strip() for article in fixtures["articles"]]
    tokenizer = AutoTokenizer.from_pretrained(MODEL, revision=REVISION)
    model = AutoModel.from_pretrained(MODEL, revision=REVISION).float().eval()
    title_lengths = [len(tokenizer.encode(article["title"], add_special_tokens=False)) for article in fixtures["articles"]]
    body_lengths = [len(tokenizer.encode(article["text"], add_special_tokens=False)) for article in fixtures["articles"]]
    query_lengths = [len(tokenizer.encode(text, add_special_tokens=False)) for text in descriptions.values()]
    lengths = [len(tokenizer.encode(text)) for text in queries + passages]
    if max(title_lengths) > 64 or max(body_lengths) > 384 or max(query_lengths) > 256 or max(lengths) > 512:
        raise ValueError("Fixtures exceed the core.ingest single-window profile; use its segmentation service instead.")

    inputs = tokenizer(queries + passages, padding=True, truncation=False, return_tensors="pt")
    with torch.inference_mode():
        hidden = model(**inputs).last_hidden_state
        hidden = hidden.masked_fill(~inputs["attention_mask"][..., None].bool(), 0.0)
        pooled = hidden.sum(dim=1) / inputs["attention_mask"].sum(dim=1)[..., None]
        embeddings = functional.normalize(pooled, p=2, dim=1)
        similarities = embeddings[len(queries):] @ embeddings[:len(queries)].T

    pairs = []
    for article_index, article in enumerate(fixtures["articles"]):
        for query_index, name in enumerate(descriptions):
            label = extra_queries[name]["fits_as"] if name in extra_queries else name
            pairs.append({"article": article_index, "language": article["lang"], "description": name,
                          "fits": label in article["fits"], "similarity": float(similarities[article_index, query_index])})
    sweep = []
    for step in range(40, 96):
        threshold = step / 100
        true_positive = sum(pair["fits"] and pair["similarity"] >= threshold for pair in pairs)
        false_positive = sum(not pair["fits"] and pair["similarity"] >= threshold for pair in pairs)
        false_negative = sum(pair["fits"] and pair["similarity"] < threshold for pair in pairs)
        sweep.append({"threshold": threshold, "true_positive": true_positive,
                      "false_positive": false_positive, "false_negative": false_negative})
    result = {"measured_at": datetime.now(timezone.utc).isoformat(), "model": MODEL, "revision": REVISION,
              "method": "CPU float32, eval, masked mean pooling, L2 normalization, cosine; official Transformers path",
              "versions": {name: importlib.metadata.version(name) for name in ("torch", "transformers", "tokenizers")},
              "max_tokens": {"title": max(title_lengths), "body": max(body_lengths), "query": max(query_lengths), "input": max(lengths)},
              "query_inputs": queries, "passage_inputs": passages, "pairs": pairs, "threshold_sweep": sweep}
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    positives = [pair["similarity"] for pair in pairs if pair["fits"]]
    negatives = [pair["similarity"] for pair in pairs if not pair["fits"]]
    print(f"{len(pairs)} pairs: {len(positives)} positives, {len(negatives)} negatives")
    print(f"Positive range: {min(positives):.6f} .. {max(positives):.6f}")
    print(f"Negative range: {min(negatives):.6f} .. {max(negatives):.6f}")
    for row in sweep:
        if row["false_positive"] == 0 and row["false_negative"] == 0:
            print(f"Separating threshold: {row['threshold']:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
