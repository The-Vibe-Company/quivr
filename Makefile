GO ?= go
.PHONY: dev check verify down reset migrate adapter-postgres test contracts generate demo demo-reset verify-demo measure eval docs start-pages denylist migrations migration migration-restamp image-context plugin-boundary

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
demo:
	GO=$(GO) python3 scripts/demo.py dev
demo-reset:
	GO=$(GO) python3 scripts/demo.py reset
verify-demo:
	GO=$(GO) python3 scripts/demo.py verify
# Everything that needs no Docker stack; run it before pushing (about two minutes on a laptop).
check: docs denylist migrations contracts image-context plugin-boundary test
# make check, then every part of the stack verification one after another, then the demo.
# make verify part=<name>[,<name>] runs only those parts, without make check; parts are listed
# in scripts/local.py (parts) and CI runs them in parallel.
verify: $(if $(part),,check)
	GO=$(GO) python3 scripts/local.py verify $(if $(part),--part $(part))
# PostgreSQL adapter suite on a bare migrated database (THE-699): make adapter-postgres [args='-run TestX']
adapter-postgres:
	GO=$(GO) python3 scripts/adapter_postgres.py $(args)
test:
	$(GO) vet ./...
	$(GO) test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 -m unittest discover -s scripts/eval -p 'test_*.py'
	GO=$(GO) bash scripts/plugin_sdk.sh
	GO=$(GO) bash scripts/plugin_sdk_go.sh
# Explicit retrieval measurement (THE-661); not part of verify.
measure:
	GO=$(GO) python3 scripts/measure.py
# Search quality on public evaluation sets (THE-775, docs/agents/evaluation.md); not part of verify.
# Needs scripts/eval/requirements.txt. make eval [args='--sets scifact --baseline <report.json>']
eval:
	GO=$(GO) python3 scripts/eval/run.py $(args)
generate:
	GO=$(GO) bash scripts/contracts.sh generate
contracts:
	GO=$(GO) bash scripts/contracts.sh check
# Fails on an undeclared or missing doc page, a broken link or a missing path, a page over its line budget,
# a malformed glossary term, a stale start page, or an edited dated doc or accepted ADR (compared with where
# the branch forked from origin/main); see docs/inventory.toml and docs/agents/documentation.md.
docs:
	python3 scripts/docs.py
# Regenerates the per-reader start pages (docs/start/) from docs/inventory.toml, then checks.
start-pages:
	python3 scripts/docs.py --write-start-pages
# Fails when a denylisted (hashed) customer term appears; see scripts/denylist.py.
denylist:
	python3 scripts/denylist.py
# Fails when code under plugins/ or sdks/go/ imports the engine's internal/ packages.
plugin-boundary:
	python3 scripts/plugin_boundary.py
# Fails when the core image build stage misses a Go package the binary imports.
image-context:
	GO=$(GO) python3 scripts/image_context.py
# Fails when a migration added here sorts before main's latest; see scripts/migrations.py.
migrations:
	python3 scripts/migrations.py check
# New migration named by the current UTC time: make migration name=<slug>
migration:
	python3 scripts/migrations.py new $(name)
# Move a migration after main's latest: make migration-restamp file=<name>.sql
migration-restamp:
	python3 scripts/migrations.py restamp $(file)
