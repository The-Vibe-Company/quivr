GO ?= go
.PHONY: dev env check verify down reset migrate adapter-postgres test test-go test-python test-eval test-sdk-python test-sdk-go contracts generate demo demo-reset verify-demo demo-perf measure measure-backfill measure-upgrade measure-archive eval load docs start-pages docs-site docs-site-check docs-preview denylist migrations migration migration-restamp image-context plugin-boundary conformance conformance-validate migration-compatibility

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
# Prints the export lines of the make dev stack's API address, key and webhook destination: eval "$$(make -s env)".
env:
	@python3 scripts/local.py env
demo:
	GO=$(GO) python3 scripts/demo.py dev
demo-reset:
	GO=$(GO) python3 scripts/demo.py reset
verify-demo:
	GO=$(GO) python3 scripts/demo.py verify
# The demo's speed against quivr-search/perf/budgets.json on a seeded stack of its own (THE-1041); not part of verify.
demo-perf:
	GO=$(GO) python3 scripts/demo.py perf
# Everything that needs no Docker stack; run it before pushing (about two minutes on a laptop).
check:
	GO=$(GO) python3 scripts/check.py $(if $(check-group),--group $(check-group))
# make check, then every part of the stack verification one after another, then the demo.
# make verify part=<name>[,<name>] runs only those parts, without make check; parts are listed
# in scripts/local.py (parts) and CI runs them in parallel.
verify: $(if $(part),,check)
	GO=$(GO) python3 scripts/local.py verify $(if $(part),--part $(part))
# PostgreSQL adapter suite on a bare migrated database (THE-699): make adapter-postgres [args='-run TestX']
adapter-postgres:
	GO=$(GO) python3 scripts/adapter_postgres.py $(args)
test: test-go test-python test-eval test-sdk-python test-sdk-go
test-go:
	$(GO) vet ./...
	GO=$(GO) python3 scripts/check.py --go .
	cd tests/fakes && $(GO) vet ./...
	GO=$(GO) python3 scripts/check.py --go tests/fakes
test-python:
	$(CONFORMANCE_PYTHON) scripts/check.py --unittest conformance --module conformance.test_runner
	python3 scripts/check.py --unittest scripts
test-eval:
	python3 scripts/check.py --unittest scripts/eval --preload ranx $(if $(eval-shard),--shard $(eval-shard))
test-sdk-python:
	GO=$(GO) bash scripts/plugin_sdk.sh
test-sdk-go:
	GO=$(GO) bash scripts/plugin_sdk_go.sh
# Explicit retrieval measurement (THE-661); not part of verify.
measure:
	GO=$(GO) python3 scripts/measure.py
# Live ingestion freshness while a backfill runs (THE-784); not part of verify.
measure-backfill:
	GO=$(GO) python3 scripts/measure_backfill.py
# A live plugin upgrade, drain, rollback and backfill under load, gated on no lost Record and no API failure
# outside api restarts (THE-786); not part of verify.
measure-upgrade:
	GO=$(GO) python3 scripts/measure_upgrade.py
# Synthetic object-storage archive throughput on an existing local demo/dev stack; outside CI.
measure-archive:
	GO=$(GO) python3 scripts/measure_archive.py $(args)
# Search quality on public evaluation sets (THE-775, docs/agents/evaluation.md); not part of verify.
# Needs scripts/eval/requirements.txt. make eval [args='--sets scifact --baseline <report.json>']
eval:
	GO=$(GO) python3 scripts/eval/run.py $(args)
# Synthetic provider load measurements, local only; never part of check or verify.
load:
	GO=$(GO) python3 scripts/load.py $(args)
generate:
	GO=$(GO) bash scripts/contracts.sh generate
contracts:
	GO=$(GO) bash scripts/contracts.sh check
# Fails on an undeclared or missing doc page, a broken link or a missing path, a page over its line budget,
# a malformed glossary term, a stale start page or documentation site, or an edited dated doc or accepted ADR
# (compared with where the branch forked from origin/main); see docs/inventory.toml and docs/agents/documentation.md.
docs:
	python3 scripts/docs.py
# Regenerates the per-reader start pages (docs/start/) from docs/inventory.toml, then checks.
start-pages:
	python3 scripts/docs.py --write-start-pages
# The public documentation site (THE-719): docs-site regenerates docs-site/ from the living pages
# (scripts/mintlify_site.py; needs PyYAML from contracts/http/v0/checks/requirements.txt). docs-site-check runs
# the pinned Mintlify CLI on it as CI does, and docs-preview serves it on http://localhost:3000 (Node 20.17+).
MINT ?= npx --yes mint@4.2.952
docs-site:
	python3 scripts/mintlify_site.py
docs-site-check:
	cd docs-site && $(MINT) validate && $(MINT) broken-links --check-anchors
docs-preview:
	cd docs-site && $(MINT) dev
# Fails when a denylisted (hashed) customer term appears; see scripts/denylist.py.
denylist:
	python3 scripts/denylist.py
# Fails when code under plugins/ or sdks/go/ imports the engine's internal/ packages.
plugin-boundary:
	python3 scripts/plugin_boundary.py
# Fails when either core image build stage misses a Go package the binary imports.
image-context:
	GO=$(GO) python3 scripts/image_context.py
# Fails when a migration added here sorts before main's latest; see scripts/migrations.py.
migrations:
	python3 scripts/migrations.py check
# Run the previous merge-base binary against the expanded schema (Linux x86_64).
migration-compatibility:
	GO=$(GO) python3 scripts/migration_compatibility.py $(args)
# New migration named by the current UTC time: make migration name=<slug>
migration:
	python3 scripts/migrations.py new $(name)
# Move a migration after main's latest: make migration-restamp file=<name>.sql
migration-restamp:
	python3 scripts/migrations.py restamp $(file)

# Local requirement measurements, never run by CI. Example: make conformance suite=example version=v2.0.0-alpha.1
CONFORMANCE_PYTHON ?= python3
conformance:
	GO=$(GO) $(CONFORMANCE_PYTHON) conformance/runner.py --suite "$(or $(suite),example)" $(if $(version),--version "$(version)") $(args)
# make check needs conformance/requirements.txt (the same pins installed by the CI contract lane).
conformance-validate:
	$(CONFORMANCE_PYTHON) conformance/runner.py --validate
