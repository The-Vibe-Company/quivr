# Prove requirements with conformance cases

Add a requirement as a YAML case, then run `make conformance` to see its measurement and evidence. You can contribute cases from an external repository by opening a pull request against this repository.

## Prerequisites

Use Python 3.12 or newer and install the pinned tooling:

```sh
python3 -m pip install -r conformance/requirements.txt
```

For an isolated stack, use Go 1.27.1, Docker with Compose v2, make, and Linux x86_64 or macOS arm64. See the [local harness guide](../docs/quivr-v2-local-harness.md). It allocates separate ports and containers, uses local fake external services and E5 embeddings, and attempts to remove every owned process and volume when finished. Teardown failures mark the run as an error and are included in both reports. It downloads pinned dependencies and model files; it needs no paid provider key.

## Add a case

1. Fork this repository, create a branch, and add one YAML file per requirement under `conformance/suites/<suite>/`. Start from the neutral [example suite](suites/example/).
2. Choose an opaque `id`, unique within your suite, that contains no organization name. Keep ids stable when editing a requirement. Cases, descriptions and thresholds must be generic; private requirements and datasets stay in your own repository.
3. Choose a maintainer-owned `check`, its `parameters` and the expected `threshold`. The [JSON Schema](schema/case.schema.json) is authoritative. Cases contain data only: no commands, imports, templates, custom YAML tags, anchors or aliases.
4. Run static validation and the generic-content guard:

```sh
make conformance-validate
make denylist
```

5. Open a pull request that adds or edits cases only. Explain what observable requirement each case proves, why the threshold is useful and which target inputs it needs. Your external repository's command for opening the pull request belongs there.

For example, a route must answer with status 204 within 1000 ms:

```yaml
id: EXNF-HTTP-001
check: http_probe
parameters:
  target: probe
  path: /healthz
threshold:
  status: 204
  max_latency_ms: 1000
```

Reviewers accept stable generic ids, a meaningful measurable requirement, strict schema validity and thresholds with a stated rationale. They review changes to check code separately as maintainer work. CI validates case schemas and runs the runner's small owner tests; it never starts a conformance stack or executes contributed cases.

## Run and read the evidence

Run the example requirements against the current working tree:

```sh
make conformance suite=example
```

The runner writes `.scratch/conformance/report.json` and `.scratch/conformance/report.md`. Each result includes its case file, check, threshold, measurement, evidence hashes and reason. Both formats record the source revision, source and harness dirtiness, harness revision and target evidence. Repeating a run replaces these reports; use `args='--output <directory>'` to keep separate runs.

| Status | Meaning |
| --- | --- |
| `met` | The observed value satisfies every threshold in that case. |
| `not met` | A measurement exists and violates the requirement. |
| `error` | The check could not finish, for example a connection or capture failed. |
| `skipped` | An input or check type is missing; the reason says which. |

The runner's exit code 0 means every measured case passed; skipped cases can remain. Exit code 1 means a requirement failed or a run/check error occurred. Exit code 2 means input/schema validation or pre-run source/filesystem setup failed. Make reports any nonzero runner exit as a failed target. An empty log stream is `not met`; an unsupplied capture is `skipped`. A skipped case never proves conformance.

The example includes a stdout logging requirement and image properties. These can fail or be skipped on today's stack. The suite records gaps as well as passes; it does not change engine behavior to satisfy them.

To build a specific Git revision, use a local tag or commit. This command was checked with `HEAD`:

```sh
make conformance suite=example version=HEAD
```

The runner archives that revision into a temporary source directory, builds it with the current harness, and removes the archive after teardown. The report distinguishes the tested source revision from the harness revision. Use revisions compatible with the current harness; this is a source-build comparison, not a release-image deployment.

To target an existing stack, replace the example addresses and paths below with its own. This example was not executed against an external deployment:

```sh
make conformance suite=example args='--api-url http://127.0.0.1:41863 \
  --probe-url http://127.0.0.1:41864 --binary /path/to/quivr \
  --stdout-log /path/to/api-stdout.log --stderr-log /path/to/api-stderr.log'
```

Set `QUIVR_CONFORMANCE_API_KEY` in your environment only for cases with `authenticated: true`. Cases cannot set headers or keys. HTTP checks use GET, bounded reads and a five-second timeout, with redirects disabled. `--timeout` changes the timeout, up to 60 seconds. The operator selects target URLs; cases select relative routes within them.

An existing target's version is the operator's responsibility. `version=` chooses the source contract to inspect and records its revision; it does not attest the deployed API version. Missing `--binary`, log captures or image inputs produce explicit skips. Use `args='--no-stack'` to inspect only supplied artifacts without starting a stack.

For image checks, select an image available to local Docker. An SBOM is an inventory of its components. Generate Syft JSON for that exact image and supply it with `--sbom`. Replace the image placeholder below; this image example was not executed:

```sh
syft docker:<image-reference> -o syft-json > /tmp/quivr-sbom.json
make conformance args='--no-stack --image <image-reference> --sbom /tmp/quivr-sbom.json'
```

The non-root check proves a numeric, nonzero configured UID; named users are skipped because inspection cannot resolve their effective UID. The image check uses Docker's inspected image id and requires the Syft inventory's `source.target.imageID` to match it. An unrelated inventory or an empty inventory fails. Reports contain hashes and field-presence measurements rather than response bodies, log messages, credentials or configuration contents. Maintainers may attach both reports to a GitHub release after reviewing the evidence.

## Check reference

Every case needs `id`, `check`, `parameters` and `threshold`; `description` and `maintainer_ticket` are optional for existing checks. Unknown checks require an HTTPS `maintainer_ticket` link. Known check parameters and thresholds reject unknown fields.

| Check | Parameters and threshold |
| --- | --- |
| `http_probe` | `path`; optional `target: api` or `probe`, `authenticated: true`. Threshold: `status`, `max_latency_ms`. Measures one GET including body read. |
| `openapi_valid` | Empty parameters. Threshold: `valid: true`. Validates the target source's OpenAPI contract with shared schemas bundled. |
| `metric_exposed` | `name`. Threshold: `type` (counter, gauge, histogram, summary, untyped). Requires a matching type declaration and sample on `/metrics`. |
| `log_format` | `stream: stdout` or `stderr`. Threshold: `fields` array. Every captured line must be a JSON object containing those fields. |
| `image_property` | Empty parameters. Threshold: one or more of `non_root: true`, `labels` array, `sbom: true`. Inspects a selected image and its bound inventory. |
| `error_shape` | Same parameters as `http_probe`. Threshold: error `status`, `fields` mapping names to JSON types. Requires JSON content type and all typed fields; strings must be nonempty. |
| `config_refuses_invalid` | `fixture`: `unknown_field`, `invalid_json` or `missing_required`. Threshold: nonzero `exit_code`, `diagnostic` substring on stderr. Runs the supplied binary's `api` command with a fixed invalid configuration and a 30-second deadline independent of HTTP. |

## Request and implement a new check

Before accepting a case whose check is missing, the reviewer opens a maintainer ticket and puts its HTTPS link in `maintainer_ticket`. Record the requirement, proposed parameters, measurement and threshold there. The runner accepts the case and reports `skipped: needs check type X`, with the ticket link, until maintainers implement it. It needs no tracker credentials and opens no tickets at runtime.

Maintainers implement a check in `conformance/checks/`, register it in `CHECKS`, and add strict parameter/threshold schemas. A check returns `Observation(met, measurement, evidence, reason)` or raises `Skip(reason)` for unavailable inputs; unexpected failures become `error`. Contributed data must never become executable code, a shell command or an import path.

Tracing, load and upgrade checks use this same interface when their maintainer work ships. Reuse the local fake-provider harness, write bounded measurements and evidence, and keep heavy runs outside CI. Add cases and owner tests with the check. The registry is deliberately closed: the runner never discovers plugins or code from suite directories.

## Next

Read the [local harness guide](../docs/quivr-v2-local-harness.md) for stack setup and the [testing standard](../docs/agents/testing.md) before contributing a check implementation.
