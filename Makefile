GO      ?= go
BIN     ?= ./bin/loudscan
FIXTURE ?= ./fixtures
SEED    ?= 17

.PHONY: all build test fixture demo clean fmt vet

all: build

build:
	$(GO) build -o $(BIN) ./cmd/loudscan

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# Deterministic fixture set: silence, stereo sine, 5.1 layout and a hot clip.
fixture: build
	$(BIN) fixture --dir $(FIXTURE) --seed $(SEED)

demo: build fixture
	@echo "--- default report ---"
	-$(BIN) scan --input $(FIXTURE) --integrated-max -16 --true-peak-max -1 --workers 2
	@echo "--- json report ---"
	-$(BIN) scan --input $(FIXTURE) --integrated-max -16 --true-peak-max -1 --workers 2 --format json

clean:
	rm -rf ./bin $(FIXTURE)
