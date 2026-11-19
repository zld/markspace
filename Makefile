BINARY   ?= /tmp/streamcheck
DEMO_DIR ?= /tmp/stream-demo
RATES    ?= 800000,1600000,3200000
SEGMENTS ?= 12
GO       ?= go

.PHONY: all build test vet fmt demo verify acceptance clean

all: build

build:
	$(GO) build -o $(BINARY) ./cmd/streamcheck

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

demo: build
	rm -rf $(DEMO_DIR)
	$(BINARY) demo --root $(DEMO_DIR) --rates $(RATES) --segments $(SEGMENTS) \
		--drop 1600000:5 --shuffle 3200000

verify: build demo
	$(BINARY) verify --root $(DEMO_DIR) --json || true

# Full acceptance run: expects exit code 1 from verify because the demo
# intentionally drops one segment and shuffles one rate.
acceptance: build
	@rm -rf $(DEMO_DIR)
	@$(BINARY) demo --root $(DEMO_DIR) --rates $(RATES) --segments $(SEGMENTS) \
		--drop 1600000:5 --shuffle 3200000 > /dev/null
	@set +e; $(BINARY) verify --root $(DEMO_DIR) --json > $(DEMO_DIR)/verify.json; \
		code=$$?; set -e; \
		if [ $$code -ne 1 ]; then echo "expected exit 1, got $$code"; exit 1; fi
	@grep -q '"rate_count": 3' $(DEMO_DIR)/verify.json
	@grep -q '"missing_count": 1' $(DEMO_DIR)/verify.json
	@grep -q '"duration_ms": 12000' $(DEMO_DIR)/verify.json
	@echo "acceptance: ok"

clean:
	rm -f $(BINARY)
