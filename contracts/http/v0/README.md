# Ingestion transport contract

`openapi.yaml` is the public transport source of truth for THE-543. The design
and its scope are in [the ingestion contract](../../../docs/quivr-v2-ingestion-contracts.md).
Generated code belongs in transport/SDK packages when implementation starts;
it is deliberately not checked into this design change.

## Selected tools and verification

| Target | Pinned tool | Verified |
| --- | --- | --- |
| Go types and net/http strict server bindings | oapi-codegen v2.8.0, runtime v1.7.0 | Compilation and 10 JSON round trips |
| Python client | OpenAPI Generator v7.25.0, `python` | Import and 10 JSON round trips |
| TypeScript client | OpenAPI Generator v7.25.0, `typescript-fetch`, TypeScript 5.9.3 | CommonJS/ESM compilation and 10 JSON round trips |
| Schema | openapi-spec-validator 0.9.0, jsonschema 4.26.0 | Full document, 10 examples, 17 boundary checks |

The generator image used was
`openapitools/openapi-generator-cli:v7.25.0@sha256:2ab0a9680222de65dc9d3baf861aa02b99e1b80c211d8221ebf3ae8f8a102524`.
Python verification used Pydantic 2.13.5, urllib3 2.7.0,
python-dateutil 2.9.0.post0 and typing-extensions 4.16.0; Go was 1.27.1.

An initial schema expressed state-dependent constraints using `oneOf` branches
beside shared properties. The Python generator rejected a valid Receipt as
matching multiple branches; TypeScript generated a state-only union that could
lose shared fields. Those constraints now use JSON Schema `if`/`then`/`else`.
Actual content variants still use discriminated `oneOf`. The negative checks
ensure the authoritative constraints remain enforced.

Generated transport types **are not complete JSON Schema validators**. Server
request validation must apply the authoritative schemas, followed by canonical
semantic checks. SDK generation does not implement batching/retry helpers,
idempotency persistence, polling, authenticated SSE parsing, or resynchronization.
In particular, use a streaming helper for SSE rather than the generated method
that reads an entire response as a string.

## Reproduce locally

Run from the repository root with Python, Go, Node/npm and Docker available.
These are explicit design checks, not a new deployment prerequisite. All generated
artifacts and dependencies remain under `.scratch/`.

```bash
contract_root="$(pwd)"
python -m venv .scratch/ingestion-checks
.scratch/ingestion-checks/bin/pip install -r contracts/http/v0/checks/requirements.txt
.scratch/ingestion-checks/bin/python contracts/http/v0/checks/validate.py
mkdir -p .scratch/ingestion-codegen/python .scratch/ingestion-codegen/typescript-fetch

for generator in python typescript-fetch; do
  docker run --rm --network none --user "$(id -u):$(id -g)" \
    -v "$contract_root/contracts/http/v0:/spec:ro" \
    -v "$contract_root/.scratch/ingestion-codegen:/out" \
    openapitools/openapi-generator-cli:v7.25.0@sha256:2ab0a9680222de65dc9d3baf861aa02b99e1b80c211d8221ebf3ae8f8a102524 \
    generate -i /spec/openapi.yaml -g "$generator" -o "/out/$generator" \
    --additional-properties packageName=quivr_client,npmName=quivr-client,npmVersion=0.0.0
done

.scratch/ingestion-checks/bin/pip install \
  pydantic==2.13.5 urllib3==2.7.0 python-dateutil==2.9.0.post0 typing-extensions==4.16.0
PYTHONPATH=.scratch/ingestion-codegen/python \
  .scratch/ingestion-checks/bin/python contracts/http/v0/checks/roundtrip.py

npm --prefix .scratch/ingestion-codegen/typescript-fetch install \
  --ignore-scripts --no-audit --no-fund --save-dev typescript@5.9.3
npm --prefix .scratch/ingestion-codegen/typescript-fetch run build
node contracts/http/v0/checks/roundtrip.cjs .scratch/ingestion-codegen/typescript-fetch

mkdir -p .scratch/ingestion-codegen/go
cp contracts/http/v0/checks/roundtrip_test.go .scratch/ingestion-codegen/go/
(
  cd .scratch/ingestion-codegen/go
  test -f go.mod || go mod init example.invalid/quivr-contract-validation
  go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 \
    -generate types,std-http,strict-server -package transport \
    "$contract_root/contracts/http/v0/openapi.yaml" > transport.gen.go
  go get github.com/oapi-codegen/runtime@v1.7.0
  EXAMPLES="$contract_root/contracts/http/v0/examples.json" go test -v ./...
)
```

The examples intentionally retain nested extension objects, lists, numbers,
Unicode, booleans and null values. Batch fixtures include malformed raw entries;
their envelope must survive transport intact so the server can reject each
entry independently. These checks do not test a running Quivr service.
