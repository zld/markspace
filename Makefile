GO ?= go
BIN ?= /tmp/tsinspect
DEMO ?= /tmp/demo.ts
PROGRAM ?= /tmp/program-1.ts

.PHONY: build demo inspect slice test fmt vet clean

build:
	$(GO) build -o $(BIN) ./cmd/tsinspect

demo: build
	$(BIN) generate --out $(DEMO) --scenario updates-loss
	$(BIN) inspect --input $(DEMO) --json

inspect: build
	$(BIN) inspect --input $(DEMO)

slice: build
	$(BIN) slice --input $(DEMO) --program 1 --out $(PROGRAM)

test:
	$(GO) test ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

vet:
	$(GO) vet ./...

clean:
	rm -f $(BIN) $(DEMO) $(PROGRAM)
