#!/usr/bin/env bash
# Python Plugin SDK checks, run by `make test`:
#  1. the generated models and schema copies match the contracts;
#  2. the SDK unit tests pass;
#  3. a plugin scaffolded by `quivr plugin init` passes inspect and its own tests,
#     `quivr plugin dev --fixture` prints a response the engine accepts,
#     `quivr plugin test` certifies it (JSON report in
#     .scratch/plugin-sdk/contract-report.json), and a discovery digest
#     mismatch is reported;
#  4. an alert-rule plugin scaffolded by `quivr plugin init --kind
#     subscription` passes inspect, its own tests, `quivr plugin dev --fixture`
#     and `quivr plugin test` (JSON report in
#     .scratch/plugin-sdk/subscription-contract-report.json);
#  5. the reference plugin plugins/pdf-text passes its tests and `quivr plugin
#     test` (JSON report in .scratch/plugin-sdk/pdf-text-contract-report.json);
#  6. the alerts plugin plugins/alerts passes its tests, replays its keyword
#     fixture and passes `quivr plugin test` with its keyword and described
#     fixtures, described alerts talking to the fake System One server, never
#     TypeSafe (JSON report in .scratch/plugin-sdk/alerts-contract-report.json).
# Needs Python 3.12+ and network access for pip (like `make contracts`).
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
GO=${GO:-go}
PYTHON=${PYTHON:-python3}
work="$PWD/.scratch/plugin-sdk"
mkdir -p "$work"

"$PYTHON" sdks/python/scripts/generate.py --check

test -x "$work/venv/bin/python" || "$PYTHON" -m venv "$work/venv"
# Pin runtime dependencies to the versions the contract checks already use.
"$work/venv/bin/pip" install -q --disable-pip-version-check -c contracts/http/v0/checks/requirements.txt -e sdks/python
"$work/venv/bin/python" -W error::ResourceWarning "$root/scripts/check.py" --unittest sdks/python/tests

"$GO" build -o "$work/quivr" ./cmd/quivr
quivr="$work/quivr"
"$work/venv/bin/python" "$root/scripts/plugin_sdk_conformance.py" python "$quivr" "$work/venv/bin/python"
e2e="$work/e2e"
rm -rf "$e2e"
mkdir -p "$e2e"
cd "$e2e"
"$quivr" plugin init demo > init.log
cd demo
export PATH="$work/venv/bin:$PATH"

"$quivr" plugin inspect . > inspect.log
python3 "$root/scripts/check.py" --unittest tests
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

# An alert-rule plugin scaffolded by `quivr plugin init --kind subscription`:
# inspect, its own tests, a replayed fixture and Contract Runner
# certification. CI uploads the JSON report.
cd "$e2e"
"$quivr" plugin init alerts --kind subscription > init-alerts.log
cd alerts
"$quivr" plugin inspect . > inspect.log
python3 "$root/scripts/check.py" --unittest tests
"$quivr" plugin dev --fixture fixtures/sample.json > response.json 2> dev.log || { cat dev.log; exit 1; }
python3 - <<'EOF'
import json
response = json.load(open("response.json"))
decisions = [(d["id"], d["decision"]) for d in response["decisions"]]
assert decisions == [("e1", "match"), ("e2", "no_match"), ("e3", "match"), ("e4", "match"), ("e5", "no_match")], decisions
assert response["decisions"][0]["evidence"]["part_keys"] == ["title", "body"], response["decisions"][0]
log = open("dev.log").read()
assert "5 decisions in 1 batches (3 match, 2 no_match, 0 not_ready)" in log, log
print("plugin dev replayed the scaffolded subscription fixture:", decisions)
EOF
"$quivr" plugin test --report "$work/subscription-contract-report.json" . > contract.log 2>&1 || { cat contract.log; exit 1; }
grep -q "^CERTIFIED" contract.log || { cat contract.log; exit 1; }
grep -q "PASS  batch" contract.log || { cat contract.log; exit 1; }
echo "quivr plugin test certified the scaffolded subscription template: $work/subscription-contract-report.json"

cd "$e2e"
"$quivr" plugin init source --kind connector > init-source.log
cd source
"$quivr" plugin inspect . > inspect.log
python3 "$root/scripts/check.py" --unittest tests
"$quivr" plugin test --report "$work/connector-contract-report.json" . > contract.log 2>&1 || { cat contract.log; exit 1; }
grep -q "^CERTIFIED" contract.log || { cat contract.log; exit 1; }
echo "quivr plugin test certified the scaffolded connector: $work/connector-contract-report.json"

# Push scaffold and neutral source, certified without external services.
cd "$e2e"
"$quivr" plugin init push-demo --kind connector --push > init-push.log
cd push-demo
python3 "$root/scripts/check.py" --unittest tests
"$quivr" plugin test --report "$work/push-scaffold-contract-report.json" . > contract.log 2>&1 || { cat contract.log; exit 1; }
grep -q "^CERTIFIED" contract.log || { cat contract.log; exit 1; }
cd "$root/plugins/push-source"
python3 "$root/scripts/check.py" --unittest tests
"$quivr" plugin test --report "$work/push-source-contract-report.json" . > "$work/push-source-contract.log" 2>&1 || { cat "$work/push-source-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/push-source-contract.log" || { cat "$work/push-source-contract.log"; exit 1; }
echo "quivr plugin test certified the push scaffold and plugins/push-source"

cd "$root/sdks/python/examples/static-source"
"$quivr" plugin inspect . > "$work/python-static-source-inspect.log"
"$quivr" plugin test --report "$work/python-static-source-contract-report.json" . > "$work/python-static-source-contract.log" 2>&1 || { cat "$work/python-static-source-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/python-static-source-contract.log" || { cat "$work/python-static-source-contract.log"; exit 1; }
echo "quivr plugin test certified the Python static source: $work/python-static-source-contract-report.json"

# The reference plugin plugins/pdf-text: unit tests (including fixture
# reproducibility) and Contract Runner certification. CI uploads the report.
cd "$root/plugins/pdf-text"
"$work/venv/bin/pip" install -q --disable-pip-version-check -c "$root/contracts/http/v0/checks/requirements.txt" -e .
python3 -W error::ResourceWarning "$root/scripts/check.py" --unittest tests
"$quivr" plugin inspect . > "$work/pdf-text-inspect.log"
"$quivr" plugin test --report "$work/pdf-text-contract-report.json" . > "$work/pdf-text-contract.log" 2>&1 || { cat "$work/pdf-text-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/pdf-text-contract.log" || { cat "$work/pdf-text-contract.log"; exit 1; }
echo "quivr plugin test certified plugins/pdf-text: $work/pdf-text-contract-report.json"

# NewsML-G2 owns field mapping/safety in its invocation tests; the Contract
# Runner independently certifies both routed media types and deterministic replay.
cd "$root/plugins/newsml-g2"
"$work/venv/bin/pip" install -q --disable-pip-version-check -c "$root/contracts/http/v0/checks/requirements.txt" -e .
python3 -W error::ResourceWarning "$root/scripts/check.py" --unittest tests
"$quivr" plugin inspect . > "$work/newsml-g2-inspect.log"
"$quivr" plugin test --fixture fixtures/sample.json --fixture fixtures/message.json --report "$work/newsml-g2-contract-report.json" . > "$work/newsml-g2-contract.log" 2>&1 || { cat "$work/newsml-g2-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/newsml-g2-contract.log" || { cat "$work/newsml-g2-contract.log"; exit 1; }
echo "quivr plugin test certified plugins/newsml-g2: $work/newsml-g2-contract-report.json"

# The first-party alerts plugin plugins/alerts: unit tests (grammar, matching,
# evidence, described alerts against the fake System One server), a replayed
# fixture and Contract Runner certification of both kinds. CI uploads the report.
cd "$root/plugins/alerts"
"$work/venv/bin/pip" install -q --disable-pip-version-check -c "$root/contracts/http/v0/checks/requirements.txt" -e .
python3 -W error::ResourceWarning "$root/scripts/check.py" --unittest tests
"$quivr" plugin inspect . > "$work/alerts-inspect.log"
"$quivr" plugin dev --fixture fixtures/sample.json > "$work/alerts-response.json" 2> "$work/alerts-dev.log" || { cat "$work/alerts-dev.log"; exit 1; }
grep -q "8 decisions in 1 batches (5 match, 2 no_match, 1 not_ready)" "$work/alerts-dev.log" || { cat "$work/alerts-dev.log"; exit 1; }
# Described alerts are certified against the fake System One server with a test key.
fake_port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
python3 -m alerts.fake_system_one --port "$fake_port" --key test-key &
fake_pid=$!
trap 'kill "$fake_pid" 2>/dev/null || true' EXIT
python3 -c 'import sys, time, urllib.request
for _ in range(100):
    try: urllib.request.urlopen(f"http://127.0.0.1:{sys.argv[1]}/requests", timeout=1).close(); break
    except OSError: time.sleep(.05)' "$fake_port"
TYPESAFE_API_KEY=test-key TYPESAFE_API_URL="http://127.0.0.1:$fake_port/v1/systemone" \
  "$quivr" plugin test --report "$work/alerts-contract-report.json" --fixture fixtures/sample.json --fixture tests/data/described.json --fixture tests/data/vectors.json --fixture tests/data/vectors-not-ready.json . > "$work/alerts-contract.log" 2>&1 || { cat "$work/alerts-contract.log"; exit 1; }
grep -q "PASS  batch            \[subscription\] tests/data/described.json" "$work/alerts-contract.log" || { cat "$work/alerts-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/alerts-contract.log" || { cat "$work/alerts-contract.log"; exit 1; }
echo "quivr plugin test certified plugins/alerts: $work/alerts-contract-report.json"

cd "$root/plugins/jev-rerank"
"$work/venv/bin/pip" install -q --disable-pip-version-check -c "$root/contracts/http/v0/checks/requirements.txt" -e .
python3 -W error::ResourceWarning "$root/scripts/check.py" --unittest tests
"$quivr" plugin inspect . > "$work/jev-rerank-inspect.log"
env -u TYPESAFE_API_KEY -u TYPESAFE_API_URL "$quivr" plugin test --report "$work/jev-rerank-contract-report.json" . > "$work/jev-rerank-contract.log" 2>&1 || { cat "$work/jev-rerank-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/jev-rerank-contract.log" || { cat "$work/jev-rerank-contract.log"; exit 1; }
echo "quivr plugin test certified plugins/jev-rerank: $work/jev-rerank-contract-report.json"
