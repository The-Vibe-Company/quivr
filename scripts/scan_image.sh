#!/usr/bin/env bash
# One vulnerability policy for local PR images and immutable release images.
set -euo pipefail
image=${1:?usage: scan_image.sh docker:image|registry:image}
export GRYPE_CHECK_FOR_APP_UPDATE=false
if grype "$image" --only-fixed --fail-on critical --output json > vulnerabilities.json; then
  exit 0
else
  status=$?
fi

if ! python3 - "$status" <<'PY'
import json
from pathlib import Path
import sys


def clean(value):
    value = " ".join(str(value or "?").split())
    return value[:160]


status = sys.argv[1]
lines = [f"grype exited with status {status}", "Critical fixed findings:"]
try:
    payload = json.loads(Path("vulnerabilities.json").read_text())
except (OSError, ValueError) as error:
    lines.append(f"- scanner JSON unavailable ({type(error).__name__})")
else:
    matches = (payload.get("matches") or []) if isinstance(payload, dict) else []
    count = 0
    for match in matches:
        if not isinstance(match, dict):
            continue
        vulnerability = match.get("vulnerability") or {}
        if not isinstance(vulnerability, dict) or clean(vulnerability.get("severity")).lower() != "critical":
            continue
        fix = vulnerability.get("fix") or {}
        if not isinstance(fix, dict):
            fix = {}
        versions = fix.get("versions") or []
        if isinstance(versions, str):
            versions = [versions]
        if not versions and str(fix.get("state", "")).lower() != "fixed":
            continue
        artifact = match.get("artifact") or {}
        if not isinstance(artifact, dict):
            artifact = {}
        fixed = ",".join(clean(version) for version in versions) or clean(fix.get("state"))
        lines.append(
            f"- name={clean(artifact.get('name'))} version={clean(artifact.get('version'))} "
            f"id={clean(vulnerability.get('id'))} fix={fixed}"
        )
        count += 1
        if count == 50:
            lines.append("- additional findings omitted")
            break
    if count == 0:
        lines.append("- none")

report = "\n".join(lines) + "\n"
Path("scan-report.txt").write_text(report)
print(report, end="")
PY
then
  printf 'grype exited with status %s; scan report unavailable\n' "$status" > scan-report.txt || true
fi
exit "$status"
