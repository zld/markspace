GO ?= go
BIN ?= /tmp/tsinspect
DEMO ?= /tmp/demo.ts

.PHONY: build demo test fmt vet clean

build:
	$(GO) build -o $(BIN) ./cmd/tsinspect

demo: build
	$(BIN) generate --out $(DEMO) --scenario updates-loss
	$(BIN) inspect --input $(DEMO) --json

test:
	$(GO) test ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

vet:
	$(GO) vet ./...

clean:
	rm -f $(BIN) $(DEMO) /tmp/program-1.ts
