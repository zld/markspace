# Convenience targets. `make acceptance` runs exactly the documented sequence.

GO ?= go

.PHONY: build test tidy acceptance damaged clean

build:
	$(GO) build ./...

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy

acceptance: tidy build test
	$(GO) run ./cmd/mediarun --generate --out ./tmp/run --resume --verify

damaged:
	$(GO) run ./cmd/mediarun --generate --out ./tmp/bad --inject truncated,checksum --verify

clean:
	rm -rf ./tmp
