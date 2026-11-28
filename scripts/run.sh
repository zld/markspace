#!/bin/sh
# Local build/test/run helper for macOS. No network services are contacted;
# the only network use is the initial Go module download described below.
set -eu

cd "$(dirname "$0")/.."

GO_VERSION="1.23"
MODULES="github.com/abema/go-mp4@v1.4.1 github.com/pion/rtp@v1.8.6"

say() { printf '%s\n' "$*"; }

check_go() {
    if ! command -v go >/dev/null 2>&1; then
        say "go not found. Install Go ${GO_VERSION} with Homebrew:"
        say "  brew install go"
        say "or download it from https://go.dev/dl/"
        exit 1
    fi
    have=$(go env GOVERSION | sed 's/^go//')
    say "go ${have} detected"
    case "$have" in
        1.2[3-9]*|1.[3-9][0-9]*) ;;
        *) say "warning: go.mod targets Go ${GO_VERSION}; ${have} may still work" ;;
    esac
}

install_deps() {
    say "fetching pinned dependencies: ${MODULES}"
    # shellcheck disable=SC2086
    go mod download ${MODULES}
    go mod verify
}

build() {
    say "building all packages"
    go build ./...
}

test_all() {
    say "running unit and end-to-end tests"
    go test ./...
}

demo() {
    say "running acceptance demo"
    go run ./cmd/regress demo --seed 17 --duration 600 --versions 3 --window 64
}

case "${1:-all}" in
    deps)  check_go; install_deps ;;
    build) check_go; build ;;
    test)  check_go; test_all ;;
    demo)  check_go; demo ;;
    all)
        check_go
        install_deps
        build
        test_all
        demo
        ;;
    *)
        say "usage: $0 [deps|build|test|demo|all]"
        exit 1
        ;;
esac
