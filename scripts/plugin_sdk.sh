#!/usr/bin/env bash
# Python Plugin SDK checks, run by `make test`:
#  1. the generated models and schema copies match the contracts;
#  2. the SDK unit tests pass;
#  3. a plugin scaffolded by `quivr plugin init` passes inspect and its own tests,
#     `quivr plugin dev --fixture` prints a response the engine accepts,
#     `quivr plugin test` certifies it (JSON report in
#     .scratch/plugin-sdk/contract-report.json), and a discovery digest
#     mismatch is reported.
# Needs Python 3.12+ and network access for pip (like `make contracts`).
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
PYTHON=${PYTHON:-python3}
work="$PWD/.scratch/plugin-sdk"
mkdir -p "$work"

"$PYTHON" sdks/python/scripts/generate.py --check

test -x "$work/venv/bin/python" || "$PYTHON" -m venv "$work/venv"
# Pin runtime dependencies to the versions the contract checks already use.
"$work/venv/bin/pip" install -q --disable-pip-version-check -c contracts/http/v0/checks/requirements.txt -e sdks/python
"$work/venv/bin/python" -W error::ResourceWarning -m unittest discover -s sdks/python/tests

"$GO" build -o "$work/quivr" ./cmd/quivr
quivr="$work/quivr"
e2e="$work/e2e"
rm -rf "$e2e"
mkdir -p "$e2e"
cd "$e2e"
"$quivr" plugin init demo > init.log
cd demo
export PATH="$work/venv/bin:$PATH"

"$quivr" plugin inspect . > inspect.log
python3 -m unittest discover -s tests
"$quivr" plugin dev --fixture fixtures/sample.json > response.json 2> dev.log || { cat dev.log; exit 1; }
python3 - <<'EOF'
import json
response = json.load(open("response.json"))
roles = [part["role"] for part in response["manifest"]["parts"]]
assert roles == ["title", "body", "body", "body"], roles
assert response["manifest"]["parts"][0]["content"]["text"] == "Quarterly field report"
assert response["extensions"]["demo.outline"] == {"schema_version": "1", "data": {"heading_count": 3, "heading_levels": ["h1", "h2"]}}, response.get("extensions")
log = open("dev.log").read()
assert "discovery matches quivr-plugin.yaml" in log, log
assert "response valid" in log, log
print("plugin dev replayed the scaffolded fixture:", roles)
EOF

# Certify the template with the Contract Runner; CI uploads the JSON report.
"$quivr" plugin test --report "$work/contract-report.json" . > contract.log 2>&1 || { cat contract.log; exit 1; }
grep -q "^CERTIFIED" contract.log || { cat contract.log; exit 1; }
echo "quivr plugin test certified the scaffolded template: $work/contract-report.json"

# Serve a stale copy of the manifest: discovery must no longer match.
cp quivr-plugin.yaml stale.yaml
python3 - <<'EOF'
from pathlib import Path
source = Path("demo/normalizer.py")
source.write_text(source.read_text().replace('/ "quivr-plugin.yaml"', '/ "stale.yaml"'))
manifest = Path("quivr-plugin.yaml")
manifest.write_text(manifest.read_text() + "# edited after the plugin was built\n")
EOF
if "$quivr" plugin dev --fixture fixtures/sample.json > /dev/null 2> mismatch.log; then
  echo "plugin dev accepted a discovery digest mismatch" >&2
  exit 1
fi
grep -q "discovery_mismatch  /manifest_digest" mismatch.log || { cat mismatch.log; exit 1; }
echo "plugin dev reported the discovery digest mismatch"
