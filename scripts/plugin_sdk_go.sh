#!/usr/bin/env bash
# Go Plugin SDK checks, run by `make test`:
#  1. the SDK's embedded schema copies match the contracts;
#  2. go vet and the SDK unit tests pass (sdks/go is its own module, which the
#     root `go test ./...` does not enter);
#  3. the sample connector sdks/go/examples/static-source passes `quivr plugin
#     inspect` and `quivr plugin test` (JSON report in
#     .scratch/plugin-sdk/static-source-contract-report.json);
#  4. every first-party Go connector plugin under plugins/ passes its own
#     tests and `quivr plugin test`. A plugin whose source needs a local fake
#     (plugins/x-list) has scripts/plugin_<id>_fixtures.py, which serves the
#     fake and writes the fixtures to certify with.
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
GO=${GO:-go}
work="$PWD/.scratch/plugin-sdk"
mkdir -p "$work"

python3 sdks/go/scripts/sync_schemas.py --check
(cd sdks/go && "$GO" vet ./... && "$GO" test ./...)

"$GO" build -o "$work/quivr" ./cmd/quivr
quivr="$work/quivr"
cd "$root/sdks/go/examples/static-source"
# Compile once so the runner's startup wait covers only the start.
"$GO" build -o /dev/null .
"$quivr" plugin inspect . > "$work/static-source-inspect.log"
"$quivr" plugin test --startup-timeout 120s --report "$work/static-source-contract-report.json" . > "$work/static-source-contract.log" 2>&1 || { cat "$work/static-source-contract.log"; exit 1; }
grep -q "^CERTIFIED" "$work/static-source-contract.log" || { cat "$work/static-source-contract.log"; exit 1; }
grep -q "PASS  credentials" "$work/static-source-contract.log" || { cat "$work/static-source-contract.log"; exit 1; }
echo "quivr plugin test certified the Go SDK sample connector: $work/static-source-contract-report.json"

# First-party Go connector plugins (plugins/<id> with a go.mod, each its own
# module on the SDK): go vet, their unit tests (parity with the connector they
# replace included), then `quivr plugin test` (JSON report in
# .scratch/plugin-sdk/<id>-contract-report.json). scripts/plugin_<id>_fixtures.py
# (the id with - as _), when present, serves the plugin's fake source until the
# script ends and prints the fixtures that point at it.
helpers=()
trap 'for pid in ${helpers[@]+"${helpers[@]}"}; do kill "$pid" 2>/dev/null || true; done' EXIT
for mod in "$root"/plugins/*/go.mod; do
  [ -e "$mod" ] || continue
  dir=$(dirname "$mod"); id=$(basename "$dir")
  cd "$dir"
  "$GO" vet ./... && "$GO" test ./...
  "$GO" build -o /dev/null .
  "$quivr" plugin inspect . > "$work/$id-inspect.log"
  fixtures=()
  helper="$root/scripts/plugin_${id//-/_}_fixtures.py"
  if [ -e "$helper" ]; then
    rm -f "$work/$id-fixtures.txt"
    python3 "$helper" "$work/$id-fixtures" > "$work/$id-fixtures.txt" &
    helpers+=($!)
    for _ in $(seq 100); do [ -s "$work/$id-fixtures.txt" ] && break; sleep .05; done
    for f in $(cat "$work/$id-fixtures.txt"); do fixtures+=(--fixture "$f"); done
    [ ${#fixtures[@]} -gt 0 ] || { echo "$helper wrote no fixtures" >&2; exit 1; }
  fi
  "$quivr" plugin test --startup-timeout 120s --report "$work/$id-contract-report.json" ${fixtures[@]+"${fixtures[@]}"} . > "$work/$id-contract.log" 2>&1 || { cat "$work/$id-contract.log"; exit 1; }
  grep -q "^CERTIFIED" "$work/$id-contract.log" || { cat "$work/$id-contract.log"; exit 1; }
  grep -q "PASS  credentials" "$work/$id-contract.log" || { cat "$work/$id-contract.log"; exit 1; }
  # A plugin that declares attachments must have them exchanged, not skipped.
  if grep -q "^    attachments:" quivr-plugin.yaml; then grep -q "PASS  attachments" "$work/$id-contract.log" || { cat "$work/$id-contract.log"; exit 1; }; fi
  echo "quivr plugin test certified the $id connector plugin: $work/$id-contract-report.json"
done
