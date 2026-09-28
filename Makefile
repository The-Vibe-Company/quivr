GO ?= go
.PHONY: dev verify down reset migrate test contracts generate demo demo-reset verify-demo measure

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
demo:
	GO=$(GO) python3 scripts/demo.py dev
demo-reset:
	GO=$(GO) python3 scripts/demo.py reset
verify-demo:
	GO=$(GO) python3 scripts/demo.py verify
verify: contracts test
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
