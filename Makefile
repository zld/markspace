# gamepack — multi-platform bitstream fragment packer
# Pure-local, macOS targets. No external services required.

GO ?= go
PACK ?= pack
DEMO_ROOT ?= /tmp/game-pack-demo

.PHONY: all tidy build vet test demo resume verify clean accept

all: build

tidy:
	$(GO) mod tidy

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

$(PACK):
	$(GO) build -o $(PACK) ./cmd/pack

demo: $(PACK)
	./$(PACK) demo --root $(DEMO_ROOT) \
		--codecs h264,h265,av1 --bitrates 500,1000 \
		--inject switch,wrap --crash-after align

resume: $(PACK)
	./$(PACK) resume --root $(DEMO_ROOT)

verify: $(PACK)
	./$(PACK) verify --root $(DEMO_ROOT)

accept:
	bash scripts/verify.sh $(DEMO_ROOT)

clean:
	bash scripts/clean.sh
