"""Calibrate the default threshold of described alerts against live Jev.

Opt-in and never run in CI: it calls TypeSafe with TYPESAFE_API_KEY, one
request per article of set.json with every description. It prints each score,
then accuracy and F1 for thresholds from 0.20 to 0.95.

    set -a; . <file with TYPESAFE_API_KEY>; set +a
    python3 calibration/calibrate.py
"""
import json
import sys
import time
from pathlib import Path

from alerts import described
from alerts.jev import Jev

SET = json.loads((Path(__file__).with_name("set.json")).read_text())


def main() -> int:
    client = Jev.from_environment()
    if client is None:
        print("TYPESAFE_API_KEY is not set", file=sys.stderr)
        return 2
    names = list(SET["descriptions"])
    pairs = []
    started = time.monotonic()
    for i, article in enumerate(SET["articles"]):
        parts = [{"key": "title", "role": "title", "text": article["title"]}, {"key": "body", "role": "body", "text": article["text"]}]
        scores = client.judge(described.article_state({"parts": parts}, {}).value, [SET["descriptions"][n] for n in names])
        for name in names:
            score = scores[SET["descriptions"][name]]
            fits = name in article["fits"]
            pairs.append((score, fits))
            print(f"article {i:2d} ({article['lang']}) {name:9s} fits={str(fits):5s} score={score:.2f}")
    print(f"{len(SET['articles'])} requests in {time.monotonic() - started:.1f} s")
    for step in range(4, 20):
        threshold = step / 20
        tp = sum(s >= threshold and f for s, f in pairs)
        fp = sum(s >= threshold and not f for s, f in pairs)
        fn = sum(s < threshold and f for s, f in pairs)
        accuracy = sum((s >= threshold) == f for s, f in pairs) / len(pairs)
        f1 = 2 * tp / (2 * tp + fp + fn) if tp else 0.0
        print(f"threshold {threshold:.2f}: accuracy {accuracy:.3f} F1 {f1:.3f} (fp {fp}, fn {fn})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
