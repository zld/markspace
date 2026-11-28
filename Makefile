# Terminal-only workflow for the mediarc kernel.
GO ?= go
OUT ?= out

.PHONY: all tidy build test demo verify clean

all: verify

tidy:
	$(GO) mod tidy

build:
	$(GO) build ./...

test:
	$(GO) test ./...

demo:
	$(GO) run ./cmd/mediarc demo --seed 7 --window 4 --max-buffer 8 --out $(OUT)

# The acceptance command from the brief, in order.
verify: tidy build test demo

clean:
	rm -rf $(OUT)
