.PHONY: deps build test demo verify acceptance clean

WORK ?= ./tmp/demo

deps:
	go mod download

build:
	./scripts/build.sh

test:
	go test ./... -count=1

demo:
	go run ./cmd/mediacore demo --work $(WORK)

verify:
	go run ./cmd/mediacore verify --work $(WORK)

acceptance:
	./scripts/acceptance.sh

clean:
	rm -rf ./tmp
