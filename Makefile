GO ?= go
BIN := ./transcode
ART := ./artifacts

.PHONY: all build demo resume fixtures acceptance clean

all: build

build:
	$(GO) mod download
	$(GO) build -o $(BIN) ./cmd/transcode

demo: build
	$(BIN) demo --duration 12s --target-kbps 1800 --segment 3s --workers 2 \
		--out $(ART) --report $(ART)/report.json

resume: build
	$(BIN) resume --state $(ART)/state.json

fixtures:
	$(GO) run ./testdata/generate.go -fixtures ./testdata/out

vet:
	$(GO) vet ./...

acceptance: build
	@set -e; \
	$(BIN) demo --duration 12s --target-kbps 1800 --segment 3s --workers 2 \
		--out $(ART) --report $(ART)/report.json; \
	echo "--- rerun (expect reuse) ---"; \
	$(BIN) demo --duration 12s --target-kbps 1800 --segment 3s --workers 2 \
		--out $(ART) --report $(ART)/report.json

clean:
	rm -rf $(BIN) $(ART)
