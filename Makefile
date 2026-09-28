GO ?= go
.PHONY: dev verify down reset migrate test contracts generate demo demo-reset verify-demo measure denylist migrations migration migration-restamp

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
demo:
	GO=$(GO) python3 scripts/demo.py dev
demo-reset:
	GO=$(GO) python3 scripts/demo.py reset
verify-demo:
	GO=$(GO) python3 scripts/demo.py verify
verify: denylist migrations contracts test
	GO=$(GO) python3 scripts/local.py verify
	GO=$(GO) python3 scripts/demo.py verify
test:
	$(GO) vet ./...
	$(GO) test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'
# Explicit retrieval measurement (THE-661); not part of verify.
measure:
	GO=$(GO) python3 scripts/measure.py
generate:
	GO=$(GO) bash scripts/contracts.sh generate
contracts:
	GO=$(GO) bash scripts/contracts.sh check
# Fails when a denylisted (hashed) customer term appears; see scripts/denylist.py.
denylist:
	python3 scripts/denylist.py
# Fails when a migration added here sorts before main's latest; see scripts/migrations.py.
migrations:
	python3 scripts/migrations.py check
# New migration named by the current UTC time: make migration name=<slug>
migration:
	python3 scripts/migrations.py new $(name)
# Move a migration after main's latest: make migration-restamp file=<name>.sql
migration-restamp:
	python3 scripts/migrations.py restamp $(file)
