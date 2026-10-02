#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
if [ "${1:-check}" = generate ]; then
  "$GO" run ./cmd/quivr-plugin-api
else
  "$GO" run ./cmd/quivr-plugin-api -check
fi
work="$PWD/.scratch/contracts"
mkdir -p "$work" internal/transport/generated client
python3 -m venv "$work/venv"
"$work/venv/bin/pip" -q install -r contracts/http/v0/checks/requirements.txt
"$work/venv/bin/python" contracts/http/v0/client_schema.py > "$work/client-openapi.yaml"
"$GO" run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -generate types,std-http,strict-server -package transport "$work/client-openapi.yaml" > "$work/transport.gen.go"
# The Go client (THE-702) comes from the same schema and generator; online commands use it only.
"$GO" run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -generate types,client -package client "$work/client-openapi.yaml" > "$work/client.gen.go"
if [ "${1:-check}" = generate ]; then
  cp "$work/transport.gen.go" internal/transport/generated/transport.gen.go
  cp "$work/client.gen.go" client/client.gen.go
  "$GO" run ./cmd/quivr-reference
  exit
fi
cmp "$work/transport.gen.go" internal/transport/generated/transport.gen.go
cmp "$work/client.gen.go" client/client.gen.go
# The generated reference pages (THE-707) come from the same contract; see cmd/quivr-reference.
"$GO" run ./cmd/quivr-reference -check
"$work/venv/bin/python" contracts/http/v0/checks/validate.py
"$work/venv/bin/python" contracts/plugins/v0/checks/validate.py
node contracts/http/v0/checks/webhook.cjs
for generator in python typescript-fetch; do
  docker run --rm --network none --user "$(id -u):$(id -g)" -v "$work:/out" \
    openapitools/openapi-generator-cli:v7.25.0@sha256:2ab0a9680222de65dc9d3baf861aa02b99e1b80c211d8221ebf3ae8f8a102524 \
    generate -i /out/client-openapi.yaml -g "$generator" -o "/out/$generator" \
    --additional-properties packageName=quivr_client,npmName=quivr-client,npmVersion=0.0.0 > "$work/$generator.log"
done
"$work/venv/bin/pip" -q install pydantic==2.13.5 urllib3==2.7.0 python-dateutil==2.9.0.post0 typing-extensions==4.16.0
PYTHONPATH="$work/python" "$work/venv/bin/python" contracts/http/v0/checks/roundtrip.py
npm --prefix "$work/typescript-fetch" install --ignore-scripts --no-audit --no-fund --save-dev typescript@5.9.3 > "$work/npm.log"
npm --prefix "$work/typescript-fetch" run build
node contracts/http/v0/checks/roundtrip.cjs "$work/typescript-fetch"
mkdir -p "$work/go"
cp "$work/transport.gen.go" contracts/http/v0/checks/roundtrip_test.go "$work/go/"
cd "$work/go"
test -f go.mod || "$GO" mod init example.invalid/quivr-contract-checks
"$GO" get github.com/oapi-codegen/runtime@v1.7.0
EXAMPLES="$OLDPWD/contracts/http/v0/examples.json" "$GO" test ./...
