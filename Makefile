.PHONY: all tidy build test acceptance clean

all: build test

tidy:
	go mod tidy

build:
	go build ./...

test:
	go test ./...

acceptance:
	./scripts/acceptance.sh

clean:
	rm -rf ./tmp
