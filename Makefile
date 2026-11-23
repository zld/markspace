# mediatool Makefile: all targets run locally with no external services.

.PHONY: all tidy build test demo verify clean fmt vet

all: build

tidy:
	go mod tidy

build:
	go build ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

# Deterministic end-to-end demo. Override SEED/FRAGMENTS/LATE/OUT as needed.
SEED       ?= 42
FRAGMENTS  ?= 12
LATE       ?= 7
OUT        ?= /tmp/media-demo

demo: build
	go run ./cmd/mediatool demo --seed $(SEED) --fragments $(FRAGMENTS) \
		--late-at $(LATE) --out $(OUT)

verify:
	go run ./cmd/mediatool verify --dir $(OUT)

clean:
	rm -rf /tmp/media-demo mediatool
