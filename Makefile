# mediakey Makefile — all targets run locally on macOS with the Go toolchain.
GO        ?= go
BIN       := bin/mediakey
DEMO_ARGS := -chunks 12 -chunk-size 4096 -rotate-every 3 -inject expired,replay,tamper

.PHONY: all build test vet demo demo-check clean encrypt-demo verify-demo

all: build

build:
	$(GO) build -o $(BIN) ./cmd/mediakey

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Race-enabled test run used before tagging a build.
test-race:
	$(GO) test -race ./...

demo: build
	$(BIN) demo $(DEMO_ARGS)

# Runs the demo and validates JSON fields, counts and failure categories.
demo-check: build
	$(BIN) demo $(DEMO_ARGS) > .demo-report.json
	python3 -I scripts/check_demo.py .demo-report.json
	@rm -f .demo-report.json

# End-to-end smoke test of the file-based encrypt/verify subcommands.
encrypt-demo: build
	mkdir -p .tmp-e2e
	head -c 20000 /dev/urandom > .tmp-e2e/sample.bin
	$(BIN) encrypt -input .tmp-e2e/sample.bin -output .tmp-e2e/manifest.json \
		-chunk-size 4096 -rotate-every 2
	$(BIN) verify -manifest .tmp-e2e/manifest.json > .tmp-e2e/report.json

verify-demo: encrypt-demo
	python3 -I -c "import json; r=json.load(open('.tmp-e2e/report.json')); \
		assert r['summary']['failed'] == 0, r; print('e2e OK:', r['summary'])"
	@rm -rf .tmp-e2e

clean:
	rm -rf bin .tmp-e2e .demo-report.json
