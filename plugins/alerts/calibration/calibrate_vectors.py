"""Measure local E5 or EmbeddingGemma cosine scores; opt-in, outside the unit-test lane."""
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
    parser.add_argument("--model", choices=("e5", "gemma"), default="e5")
    args = parser.parse_args()
    if not 1 <= args.threads <= 32:
        parser.error("threads must be between 1 and 32")
    if args.output.exists():
        parser.error("output already exists; use a new path")

    import torch
    import torch.nn.functional as functional
    from transformers import AutoModel, AutoTokenizer

    torch.set_num_threads(args.threads)
    fixtures = json.loads(Path(__file__).with_name("set.json").read_text())
    extra_queries = json.loads(Path(__file__).with_name("vector-queries.json").read_text())
    descriptions = {**fixtures["descriptions"], **{name: query["description"] for name, query in extra_queries.items()}}
    if args.model == "gemma":
        from sentence_transformers import SentenceTransformer
        model_id = "google/embeddinggemma-2"
        revision = "914f7f89142e33e77833254d9c9b90c3cef7303b"
        queries = ["task: search result | query: " + text.strip().replace("\r\n", "\n").replace("\r", "\n")
                   for text in descriptions.values()]
        passages = ["title: " + article["title"].strip() + " | text: " + article["text"].strip()
                    for article in fixtures["articles"]]
        model = SentenceTransformer(model_id, revision=revision, device="cpu",
            config_kwargs={"vision_config": None, "audio_config": None},
            model_kwargs={"torch_dtype": torch.float32})
        tokenizer = model.tokenizer
        dimensions, max_input = 768, model.max_seq_length
        method = "CPU float32, eval, official SentenceTransformers text encoder, mean pooling/projection, L2 normalization, cosine"
    else:
        model_id, revision = MODEL, REVISION
        queries = ["query: " + text.strip().replace("\r\n", "\n").replace("\r", "\n") for text in descriptions.values()]
        passages = ["passage: " + article["title"].strip() + "\n\n" + article["text"].strip() for article in fixtures["articles"]]
        tokenizer = AutoTokenizer.from_pretrained(model_id, revision=revision)
        model = AutoModel.from_pretrained(model_id, revision=revision).float().eval()
        dimensions, max_input = 384, 512
        method = "CPU float32, eval, masked mean pooling, L2 normalization, cosine; official Transformers path"
    title_lengths = [len(tokenizer.encode(article["title"], add_special_tokens=False)) for article in fixtures["articles"]]
    body_lengths = [len(tokenizer.encode(article["text"], add_special_tokens=False)) for article in fixtures["articles"]]
    query_lengths = [len(tokenizer.encode(text, add_special_tokens=False)) for text in descriptions.values()]
    lengths = [len(tokenizer.encode(text)) for text in queries + passages]
    if max(title_lengths) > 64 or max(body_lengths) > 384 or max(query_lengths) > 256 or max(lengths) > max_input:
        raise ValueError("Fixtures exceed a single window; use the ingestion segmentation service instead.")

    if args.model == "gemma":
        model.eval()
        # Inputs already contain the hosted plugin templates; do not prefix twice.
        embeddings = model.encode(queries + passages, prompt="", batch_size=4, show_progress_bar=False,
                                  convert_to_tensor=True, normalize_embeddings=True)
    else:
        inputs = tokenizer(queries + passages, padding=True, truncation=False, return_tensors="pt")
        with torch.inference_mode():
            hidden = model(**inputs).last_hidden_state
            hidden = hidden.masked_fill(~inputs["attention_mask"][..., None].bool(), 0.0)
            pooled = hidden.sum(dim=1) / inputs["attention_mask"].sum(dim=1)[..., None]
            embeddings = functional.normalize(pooled, p=2, dim=1)
    if embeddings.shape != (len(queries) + len(passages), dimensions) or not torch.isfinite(embeddings).all():
        raise ValueError("Unexpected embedding shape or nonfinite output")
    similarities = embeddings[len(queries):] @ embeddings[:len(queries)].T

    pairs = []
    for article_index, article in enumerate(fixtures["articles"]):
        for query_index, name in enumerate(descriptions):
            label = extra_queries[name]["fits_as"] if name in extra_queries else name
            pairs.append({"article": article_index, "language": article["lang"], "description": name,
                          "fits": label in article["fits"], "similarity": float(similarities[article_index, query_index])})
    sweep = []
    for step in range(20, 96):
        threshold = step / 100
        true_positive = sum(pair["fits"] and pair["similarity"] >= threshold for pair in pairs)
        false_positive = sum(not pair["fits"] and pair["similarity"] >= threshold for pair in pairs)
        false_negative = sum(pair["fits"] and pair["similarity"] < threshold for pair in pairs)
        sweep.append({"threshold": threshold, "true_positive": true_positive,
                      "false_positive": false_positive, "false_negative": false_negative})
    result = {"measured_at": datetime.now(timezone.utc).isoformat(), "model": model_id, "revision": revision, "dimensions": dimensions, "threads": args.threads,
              "method": method,
              "versions": {name: importlib.metadata.version(name) for name in ("torch", "transformers", "tokenizers")},
              "max_tokens": {"title": max(title_lengths), "body": max(body_lengths), "query": max(query_lengths), "input": max(lengths)},
              "query_inputs": queries, "passage_inputs": passages, "pairs": pairs, "threshold_sweep": sweep}
    if args.model == "gemma":
        result["versions"]["sentence-transformers"] = importlib.metadata.version("sentence-transformers")
    with args.output.open("x") as output:
        output.write(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
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
