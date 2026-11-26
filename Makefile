.PHONY: build test acceptance fixture

build:
	go build ./...

test:
	go test ./...

fixture:
	go run ./cmd/regress fixture --out /tmp/live-regression --case all

acceptance:
	./scripts/acceptance.sh /tmp/live-regression
