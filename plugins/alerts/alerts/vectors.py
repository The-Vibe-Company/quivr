"""Local meaning checks over the core's stored Part and query vectors."""
from __future__ import annotations

import math
from typing import Any

from quivr_plugin import Decision, Evaluation, TerminalError, match, no_match, not_ready


def _unit(vector: Any) -> list[float] | None:
    if not isinstance(vector, list) or not vector:
        return None
    if any(isinstance(value, bool) or not isinstance(value, (int, float)) for value in vector):
        return None
    try:
        values = [float(value) for value in vector]
    except (OverflowError, ValueError):
        return None
    if not all(math.isfinite(value) for value in values):
        return None
    scale = max(abs(value) for value in values)
    if scale == 0:
        return None
    scaled = [value / scale for value in values]
    norm = math.sqrt(math.fsum(value * value for value in scaled))
    return [value / norm for value in scaled]


def decide(record: dict[str, Any], evaluation: Evaluation, configuration: dict[str, Any]) -> Decision:
    space = record.get("vector_space_id")
    query = evaluation.to_dict().get("query_vector")
    if (record.get("vectors_ready") is not True or not isinstance(space, str) or not space
            or not isinstance(query, dict) or query.get("vector_space_id") != space):
        return not_ready(evaluation, "Waiting for article and query vectors in the same vector space.")
    query_vector = _unit(query.get("vector"))
    if query_vector is None:
        return not_ready(evaluation, "Waiting for a valid query vector.")
    best = None
    for part in record.get("parts", []):
        for segment in part.get("vectors") or []:
            vector = _unit(segment.get("vector"))
            if vector is None or len(vector) != len(query_vector):
                continue
            similarity = max(-1.0, min(1.0, math.fsum(left * right for left, right in zip(query_vector, vector))))
            if best is None or similarity > best[0]:
                best = (similarity, part["key"], segment["segment_id"])
    if best is None:
        return not_ready(evaluation, "Waiting for valid article vectors with the query's dimensions.")
    similarity, part_key, segment_id = best
    threshold = evaluation.configuration.get("threshold", configuration.get("vectors", {}).get("thresholds", {}).get(space))
    if threshold is None:
        raise TerminalError("vector_threshold_required", f"Set vectors.thresholds[{space!r}] on the alerts plugin "
                            "or threshold on this Subscription; cosine thresholds must be calibrated for the vector space.")
    threshold = float(threshold)
    if similarity < threshold:
        return no_match(evaluation, f"Best vector similarity {similarity:.4f} is below the threshold {threshold:.4f}.")
    details = {"kind": evaluation.expression["kind"], "meaning_check": "vectors", "similarity": round(similarity, 6),
               "threshold": threshold, "vector_space_id": space, "segment_id": segment_id}
    return match(evaluation, f"Part {part_key}, segment {segment_id} fits the description: "
                 f"vector similarity {similarity:.4f}, threshold {threshold:.4f}.", part_keys=[part_key], details=details)
