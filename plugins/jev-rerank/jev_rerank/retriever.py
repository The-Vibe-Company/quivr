"""Hybrid candidate selection, pair caching, and optional Noul/RRF ranking."""
from __future__ import annotations

import hashlib
import logging
import os
import threading
import time
from collections import OrderedDict

from quivr_plugin.errors import TerminalError

from .client import Jev, MODEL, RUBRIC_VERSION, Result

TOKENIZER_SHA256 = "0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39"
log = logging.getLogger("quivr_plugin.jev_rerank")


class Retriever:
    def __init__(self) -> None:
        self.cache: OrderedDict[tuple, float] = OrderedDict()
        self.lock = threading.Lock()
        self.tokenizers = {}

    def trim(self, text: str, count: str, path: str, digest: str) -> str:
        if count == "full":
            return text
        with self.lock:
            tokenizer = self.tokenizers.get((path, digest))
            if tokenizer is None:
                import tokenizers
                from tokenizers import Tokenizer
                if tokenizers.__version__ != "0.23.2":
                    raise ValueError("tokenizer version")
                with open(path, "rb") as source:
                    raw = source.read()
                if hashlib.sha256(raw).hexdigest() != digest:
                    raise ValueError("tokenizer digest")
                tokenizer = Tokenizer.from_str(raw.decode())
                tokenizer.no_truncation()
                tokenizer.no_padding()
                self.tokenizers = {(path, digest): tokenizer}
        encoding = tokenizer.encode(text, add_special_tokens=False)
        bound = int(count)
        if len(encoding.offsets) <= bound:
            return text
        retained = bound
        while retained:
            end = encoding.offsets[retained - 1][1]
            if encoding.offsets[retained][0] < end:
                retained -= 1
                continue
            prefix = text[:end]
            if len(tokenizer.encode(prefix, add_special_tokens=False).ids) <= bound:
                return prefix
            retained -= 1
        return ""

    def search(self, request):
        start = time.monotonic()
        config = request.configuration
        count, trim, ranking = (str(config.get("candidate_count", 30)), str(config.get("trim_tokens", "full")), config.get("ranking", "noul"))
        if request.round == 1:
            candidate = {"query_text": request.query.text, "k": int(count) if request.profile != "default" else request.limit}
            mode = request.query.mode if request.profile == "default" else "hybrid"
            if mode == "lexical":
                candidate.update(primitive="bm25", field="source")
            else:
                space = next((space.id for space in request.spaces if space.role == "served"), None)
                if space is None:
                    raise TerminalError("no_served_space", "the searched Corpora serve no vector space", status=422)
                candidate.update(primitive="near_vector" if mode == "semantic" else "hybrid", space=space)
                if mode != "semantic":
                    candidate.update(field="source", alpha=0.5, fusion="relative_score")
            return {"requests": [candidate]}
        candidates = [candidate for served in request.served for candidate in served.candidates]
        candidates.sort(key=lambda candidate: (-candidate.score, candidate.segment_id))
        if request.profile == "default":
            primitive = request.served[0].request if request.served else None
            explanation = "keywords"
            if primitive is not None and primitive.primitive == "near_vector":
                explanation = "vectors in " + primitive.space
            elif primitive is not None and primitive.primitive == "hybrid":
                explanation = f"keywords and vectors in {primitive.space}, alpha {primitive.alpha:g}, relative score fusion"
            return self.answer(candidates, request.limit, {}, explanation, Result())
        unique = []
        for candidate in candidates:
            if any(candidate.record_id == kept.record_id and candidate.version_id == kept.version_id
                   and candidate.part_key == kept.part_key and candidate.start < kept.end and kept.start < candidate.end
                   for kept in unique):
                continue
            unique.append(candidate)
        unique = unique[:int(count)]
        scores, keys, passages = {}, {}, {}
        normalized = " ".join(request.query.text.split())
        digest = config.get("tokenizer_sha256", TOKENIZER_SHA256)
        namespace = (request.organization_id, MODEL, RUBRIC_VERSION, trim, digest, normalized)
        cache_hits = 0
        capacity = int(config.get("cache_entries", 4096))
        with self.lock:
            while len(self.cache) > capacity:
                self.cache.popitem(last=False)
            for position, candidate in enumerate(unique):
                key = (*namespace, candidate.segment_id)
                name = f"p{position}"
                keys[name] = key
                if key in self.cache:
                    scores[candidate.segment_id] = self.cache[key]
                    self.cache.move_to_end(key)
                    cache_hits += 1
                else:
                    passages[name] = candidate.text
        result = Result()
        if passages:
            key = os.environ.get("TYPESAFE_API_KEY", "").strip()
            if not key:
                result.reason = "API key not configured"
            else:
                try:
                    passages = {position: self.trim(text, trim, config.get("tokenizer_path", ""), digest) for position, text in passages.items()}
                except (OSError, ValueError, ImportError):
                    result.reason = "tokenizer unavailable"
                if not result.reason:
                    result = Jev(key, os.environ.get("TYPESAFE_API_URL", "").strip() or "https://api.typesafe.ai/v1/systemone").judge(
                        normalized, passages, start + 2.0)
                if not result.reason:
                    with self.lock:
                        for position, probability in result.scores.items():
                            scores[unique[int(position[1:])].segment_id] = probability
                            if capacity:
                                self.cache[keys[position]] = probability
                                self.cache.move_to_end(keys[position])
                        while len(self.cache) > capacity:
                            self.cache.popitem(last=False)
        log.info("Jev rerank round", extra={"event": "jev_rerank", "profile": request.profile,
                 "k": int(count), "trim": trim, "ranking": ranking, "model": result.model or MODEL,
                 "rubric": RUBRIC_VERSION, "paid_calls": result.paid_calls, "cost_cents": result.cost_cents,
                 "input_tokens": result.input_tokens, "pairs": len(unique), "cache_hits": cache_hits,
                 "estimated_tokens": result.estimated_tokens,
                 "fallback": bool(result.reason), "reason": result.reason})
        if result.reason:
            return self.answer(candidates, request.limit, {}, "re-ranker unavailable: " + result.reason, result)
        ordered = sorted(unique, key=lambda candidate: (-scores[candidate.segment_id], -candidate.score, candidate.segment_id))
        fused = None
        if ranking == "rrf":
            noul_rank = {candidate.segment_id: position + 1 for position, candidate in enumerate(ordered)}
            hybrid_rank = {candidate.segment_id: position + 1 for position, candidate in enumerate(unique)}
            fused = {candidate.segment_id: 1 / (60 + noul_rank[candidate.segment_id]) + 1 / (60 + hybrid_rank[candidate.segment_id]) for candidate in unique}
            ordered.sort(key=lambda candidate: (-fused[candidate.segment_id], -candidate.score, candidate.segment_id))
        return self.answer(ordered, request.limit, scores, f"Jev {MODEL}, rubric {RUBRIC_VERSION}, {ranking}", result, fused)

    @staticmethod
    def answer(candidates, limit, scores, explanation, usage, ranking_scores=None):
        return {"ranking": {"hits": [{"segment_id": candidate.segment_id,
                "score": (ranking_scores or scores).get(candidate.segment_id, candidate.score),
                "explanation": explanation + (f"; noul={scores[candidate.segment_id]:.6g}" if candidate.segment_id in scores else "")}
                for candidate in candidates[:limit]]},
                "usage": {"paid_calls": usage.paid_calls, "cost_cents": usage.cost_cents}}
