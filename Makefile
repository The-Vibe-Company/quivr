GO ?= go
.PHONY: dev verify down reset migrate adapter-postgres test contracts generate demo demo-reset verify-demo measure docs denylist migrations migration migration-restamp image-context

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
demo:
	GO=$(GO) python3 scripts/demo.py dev
demo-reset:
	GO=$(GO) python3 scripts/demo.py reset
verify-demo:
	GO=$(GO) python3 scripts/demo.py verify
verify: docs denylist migrations contracts image-context test
	GO=$(GO) python3 scripts/local.py verify
	GO=$(GO) python3 scripts/demo.py verify
# PostgreSQL adapter suite on a bare migrated database (THE-699): make adapter-postgres [args='-run TestX']
adapter-postgres:
	GO=$(GO) python3 scripts/adapter_postgres.py $(args)
test:
	$(GO) vet ./...
	$(GO) test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'
	GO=$(GO) bash scripts/plugin_sdk.sh
# Explicit retrieval measurement (THE-661); not part of verify.
measure:
	GO=$(GO) python3 scripts/measure.py
generate:
	GO=$(GO) bash scripts/contracts.sh generate
contracts:
	GO=$(GO) bash scripts/contracts.sh check
# Fails on an undeclared or missing doc page, a broken link or a missing path, a page over its line budget
# or a malformed glossary term; see docs/inventory.toml and docs/agents/documentation.md.
docs:
	python3 scripts/docs.py
# Fails when a denylisted (hashed) customer term appears; see scripts/denylist.py.
denylist:
	python3 scripts/denylist.py
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
