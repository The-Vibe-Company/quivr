#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
GO=${GO:-go}
python3 scripts/ci_fetch.py -- "$GO" mod download
if [ "${1:-check}" = generate ]; then
  GOPROXY=off "$GO" run ./cmd/quivr-plugin-api
else
  GOPROXY=off "$GO" run ./cmd/quivr-plugin-api -check
fi
work="$PWD/.scratch/contracts"
mkdir -p "$work" internal/transport/generated client
python3 -m venv "$work/venv"
python3 scripts/ci_fetch.py -- "$work/venv/bin/pip" install -q --disable-pip-version-check \
  --require-hashes -r contracts/http/v0/checks/requirements-lock.txt
"$work/venv/bin/python" contracts/http/v0/client_schema.py > "$work/transport-openapi.yaml"
"$work/venv/bin/python" contracts/http/v0/client_schema.py --opaque-plugin-responses > "$work/client-openapi.yaml"
GOBIN="$work/bin" python3 scripts/ci_fetch.py -- "$GO" install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
generator="$work/bin/oapi-codegen"
"$generator" -config scripts/http-bindings/config.yaml "$work/transport-openapi.yaml" > "$work/transport.gen.go"
# The Go client (THE-702) comes from the same schema and generator; online commands use it only.
"$generator" -generate types,client -package client "$work/client-openapi.yaml" > "$work/client.gen.go"
if [ "${1:-check}" = generate ]; then
  cp "$work/transport.gen.go" internal/transport/generated/transport.gen.go
  cp "$work/client.gen.go" client/client.gen.go
  GOPROXY=off "$GO" run ./cmd/quivr-reference
  exit
fi
cmp "$work/transport.gen.go" internal/transport/generated/transport.gen.go
cmp "$work/client.gen.go" client/client.gen.go
# The generated reference pages (THE-707) come from the same contract; see cmd/quivr-reference.
GOPROXY=off "$GO" run ./cmd/quivr-reference -check
"$work/venv/bin/python" contracts/http/v0/checks/validate.py
"$work/venv/bin/python" contracts/plugins/v0/checks/validate.py
node contracts/http/v0/checks/webhook.cjs
generator_image='openapitools/openapi-generator-cli:v7.25.0@sha256:2ab0a9680222de65dc9d3baf861aa02b99e1b80c211d8221ebf3ae8f8a102524'
python3 scripts/ci_fetch.py -- docker pull "$generator_image"
for generator in python typescript-fetch; do
  docker run --rm --network none --user "$(id -u):$(id -g)" -v "$work:/out" \
    --pull=never "$generator_image" \
    generate -i /out/client-openapi.yaml -g "$generator" -o "/out/$generator" \
    --additional-properties packageName=quivr_client,npmName=quivr-client,npmVersion=0.0.0 > "$work/$generator.log"
done
PYTHONPATH="$work/python" "$work/venv/bin/python" contracts/http/v0/checks/roundtrip.py
cp contracts/http/v0/checks/typescript-package.json "$work/package.json"
cp contracts/http/v0/checks/typescript-package-lock.json "$work/package-lock.json"
python3 scripts/ci_fetch.py -- npm ci --prefix "$work" --ignore-scripts --no-audit --no-fund
"$work/node_modules/.bin/tsc" --project "$work/typescript-fetch"
node contracts/http/v0/checks/roundtrip.cjs "$work/typescript-fetch"
mkdir -p "$work/go"
cp contracts/http/v0/checks/go.mod contracts/http/v0/checks/go.sum "$work/go/"
cp "$work/transport.gen.go" contracts/http/v0/checks/roundtrip_test.go "$work/go/"
(
  cd "$work/go"
  python3 "$root/scripts/ci_fetch.py" -- "$GO" mod download
  EXAMPLES="$root/contracts/http/v0/examples.json" GOPROXY=off "$GO" test -mod=readonly ./...
)
