#!/usr/bin/env bash
# Go Plugin SDK checks, run by `make test`:
#  1. the SDK's embedded schema copies match the contracts;
#  2. go vet and the SDK unit tests pass (sdks/go is its own module, which the
#     root `go test ./...` does not enter);
#  3. the sample connector sdks/go/examples/static-source passes `quivr plugin
#     inspect` and `quivr plugin test` (JSON report in
#     .scratch/plugin-sdk/static-source-contract-report.json).
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
