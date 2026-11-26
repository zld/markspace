#!/usr/bin/env bash
set -euo pipefail

# Run from the project directory: bash scripts/acceptance.sh
brew install go@1.23
export PATH="$(brew --prefix go@1.23)/bin:$PATH"

go version
go mod tidy
go mod vendor

go build -o /tmp/archivecheck ./cmd/archivecheck
go test ./...

go run ./cmd/archivecheck demo \
  --case all \
  --seed 17 \
  --queue 2 \
  --memory 67108864 \
  --slow-ms 2
