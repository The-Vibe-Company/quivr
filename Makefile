GO ?= go
.PHONY: dev verify down reset migrate test contracts generate

dev down reset migrate:
	GO=$(GO) python3 scripts/local.py $@
verify: contracts test
	GO=$(GO) python3 scripts/local.py verify
test:
	$(GO) vet ./...
	$(GO) test ./...
generate:
	GO=$(GO) bash scripts/contracts.sh generate
contracts:
	GO=$(GO) bash scripts/contracts.sh check
