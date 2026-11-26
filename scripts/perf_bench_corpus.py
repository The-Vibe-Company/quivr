"""Deterministic, domain-neutral synthetic corpus and query generator for perf_bench.

Produces reproducible multi-paragraph documents and distinct cold/warm query sets
using only stdlib primitives and generic public-interest topics. Zero customer
data, zero external downloads.
"""

from __future__ import annotations

import hashlib
import json
import random

# Neutral, generic topics and vocabulary spanning distinct semantic domains so
# lexical, semantic and hybrid searches hit varied document subsets.
TOPICS: tuple[tuple[str, tuple[str, ...]], ...] = (
    (
        "hydrology",
        ("watershed", "aquifer", "reservoir", "tributary", "runoff", "gauge", "sediment", "basin", "discharge", "groundwater"),
    ),
    (
        "railways",
        ("signalling", "timetable", "locomotive", "viaduct", "switchgear", "catenary", "ballast", "junction", "interlocking", "corridor"),
    ),
    (
        "forestry",
        ("canopy", "silviculture", "watershed", "understory", "conifer", "timber", "reforestation", "clearing", "sapling", "watershed"),
    ),
    (
        "maritime",
        ("harbour", "breakwater", "navigation", "dredging", "anchorage", "buoy", "tugboat", "lighthouse", "estuary", "berth"),
    ),
    (
        "microgrids",
        ("inverter", "substation", "photovoltaic", "storage", "frequency", "feeder", "transformer", "dispatch", "battery", "curtailment"),
    ),
    (
        "meteorology",
        ("barometer", "precipitation", "updraft", "radiosonde", "frost", "isobar", "visibility", "anemometer", "stratosphere", "dewpoint"),
    ),
    (
        "botany",
        ("herbarium", "pollination", "rhizome", "germination", "bryophyte", "cultivar", "perennial", "specimen", "taxonomy", "foliage"),
    ),
    (
        "acoustics",
        ("reverberation", "baffle", "decibel", "resonance", "absorption", "transducer", "diffraction", "attenuation", "chamber", "harmonics"),
    ),
    (
        "geology",
        ("stratum", "limestone", "outcrop", "alluvium", "faultline", "basalt", "core", "mineralogy", "erosion", "bedrock"),
    ),
    (
        "waterworks",
        ("filtration", "chlorination", "pumping", "pressure", "distribution", "clarifier", "valve", "intake", "metering", "conduit"),
    ),
    (
        "cartography",
        ("contour", "meridian", "projection", "elevation", "geodesy", "benchmark", "traverse", "triangulation", "datum", "topography"),
    ),
    (
        "astronomy",
        ("ephemeris", "photometry", "aperture", "spectroscopy", "transit", "observatory", "parallax", "zenith", "nebula", "meridian"),
    ),
)

REGIONS: tuple[str, ...] = (
    "northern-ridge",
    "coastal-plain",
    "central-valley",
    "eastern-plateau",
    "western-basin",
    "highland-terrace",
    "southern-delta",
    "upland-corridor",
)

ASPECTS: tuple[str, ...] = (
    "seasonal inspection",
    "capacity survey",
    "maintenance bulletin",
    "field calibration",
    "baseline measurement",
    "operational digest",
    "monitoring review",
    "technical archive",
)

CONNECTORS: tuple[str, ...] = (
    "recorded steady measurements across",
    "documented seasonal variations near",
    "completed routine verification along",
    "summarized quarterly observations covering",
    "published comparative readings from",
    "updated long-term instruments throughout",
)


def _clean_terms(terms: tuple[str, ...]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for term in terms:
        if term not in seen:
            seen.add(term)
            out.append(term)
    return out


def generate_documents(count: int, words_per_doc: int, seed: int, content_marker: str) -> list[dict]:
    """Generate `count` deterministic neutral documents with title and body parts."""
    if count < 0:
        raise ValueError("count must be non-negative")
    if words_per_doc < 16:
        raise ValueError("words_per_doc must be at least 16")
    docs: list[dict] = []
    for index in range(count):
        rng = random.Random(seed * 1_000_003 + index)
        topic_name, raw_terms = TOPICS[index % len(TOPICS)]
        terms = _clean_terms(raw_terms)
        secondary_name, raw_secondary = TOPICS[(index * 7 + 3) % len(TOPICS)]
        secondary_terms = _clean_terms(raw_secondary)
        region = REGIONS[(index // len(TOPICS) + index) % len(REGIONS)]
        aspect = ASPECTS[(index * 3 + 1) % len(ASPECTS)]
        marker = f"zqbench{content_marker}{index:06d}"

        title = f"{topic_name.title()} {aspect} for {region} ({index + 1:04d})"
        paragraphs: list[str] = []
        word_budget = words_per_doc
        para_idx = 0
        while word_budget > 0:
            para_words: list[str] = []
            if para_idx == 0:
                connector = CONNECTORS[index % len(CONNECTORS)]
                intro = (
                    f"Reference {marker} reports {topic_name} {aspect} in {region} "
                    f"with {secondary_name} coordination and {connector} local stations."
                )
                para_words.extend(intro.split())
            target = min(word_budget, rng.randint(45, 75))
            while len(para_words) < target:
                primary = rng.choice(terms)
                other = rng.choice(terms)
                cross = rng.choice(secondary_terms)
                sentence = (
                    f"The {primary} sensor reported stable {other} readings alongside "
                    f"{cross} observations in {region} sector {rng.randint(1, 24)}."
                )
                para_words.extend(sentence.split())
            if len(para_words) > word_budget:
                para_words = para_words[:word_budget]
            paragraphs.append(" ".join(para_words))
            word_budget -= len(para_words)
            para_idx += 1

        body = "\n\n".join(paragraphs)
        docs.append(
            {
                "index": index,
                "record_key": f"bench-{content_marker}-{index:06d}",
                "marker": marker,
                "topic": topic_name,
                "region": region,
                "title": title,
                "body": body,
                "parts": [
                    {"key": "title", "role": "title", "content": {"kind": "text", "text": title}},
                    {"key": "body", "role": "body", "content": {"kind": "text", "text": body}},
                ],
            }
        )
    return docs


def generate_cold_queries(count: int, seed: int) -> list[dict]:
    """Generate `count` strictly unique neutral queries so caches miss during cold search."""
    if count < 0:
        raise ValueError("count must be non-negative")
    rng = random.Random(seed ^ 0x5A5A_1234)
    queries: list[dict] = []
    seen: set[str] = set()
    attempt = 0
    while len(queries) < count:
        topic_name, raw_terms = TOPICS[(attempt + rng.randrange(len(TOPICS))) % len(TOPICS)]
        terms = _clean_terms(raw_terms)
        t1, t2 = rng.sample(terms, 2)
        region = REGIONS[rng.randrange(len(REGIONS))]
        aspect = ASPECTS[rng.randrange(len(ASPECTS))]
        sector = rng.randint(1, 24)
        text = f"{topic_name} {t1} {t2} {aspect} {region} sector {sector}"
        if text in seen:
            text = f"{text} pass {attempt}"
        seen.add(text)
        queries.append({"id": f"cold-{len(queries):04d}", "topic": topic_name, "query": text})
        attempt += 1
    return queries


def generate_warm_queries(count: int, seed: int) -> list[dict]:
    """Generate `count` deterministic warm working-set queries (repeated across passes)."""
    if count < 0:
        raise ValueError("count must be non-negative")
    rng = random.Random(seed ^ 0x3C3C_5678)
    queries: list[dict] = []
    seen: set[str] = set()
    index = 0
    while len(queries) < count:
        topic_name, raw_terms = TOPICS[index % len(TOPICS)]
        terms = _clean_terms(raw_terms)
        t1, t2 = rng.sample(terms, 2)
        region = REGIONS[(index * 3) % len(REGIONS)]
        aspect = ASPECTS[(index * 5) % len(ASPECTS)]
        text = f"{topic_name} {t1} {t2} {aspect} {region}"
        if text in seen:
            text = f"{text} series {index}"
        seen.add(text)
        queries.append({"id": f"warm-{len(queries):04d}", "topic": topic_name, "query": text})
        index += 1
    return queries


def workload_digest(docs: list[dict], cold_queries: list[dict], warm_queries: list[dict]) -> str:
    """Stable SHA-256 fingerprint of the generated corpus and query sets."""
    payload = json.dumps(
        {
            "docs": [(d["record_key"], d["title"], d["body"]) for d in docs],
            "cold": [q["query"] for q in cold_queries],
            "warm": [q["query"] for q in warm_queries],
        },
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    return hashlib.sha256(payload).hexdigest()
