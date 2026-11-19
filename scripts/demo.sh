#!/usr/bin/env bash
#
# demo.sh - end-to-end demonstration of the rtspdiag local diagnostic tool.
#
# It builds the binary, runs the same deterministic fixture over both UDP and
# RTP-over-TCP, and prints the JSON report for each. Everything is local: the
# mock RTSP server binds 127.0.0.1 on an ephemeral port and is torn down on exit.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BIN="${ROOT_DIR}/.tmp/rtspdiag"

echo "==> Building rtspdiag"
(
	cd "${ROOT_DIR}"
	go build -o "${BIN}" ./cmd/rtspdiag
)

echo
echo "==> Running test suite"
(
	cd "${ROOT_DIR}"
	go test ./...
)

run() {
	local transport="$1"
	echo
	echo "==> Diagnostic run over ${transport}"
	"${BIN}" run \
		--transport "${transport}" \
		--seed 42 \
		--window 8 \
		--timeout 25ms \
		--json
	echo "exit code: $?"
}

run udp
run tcp

echo
echo "==> Invalid parameter demonstration (expected exit code 2)"
set +e
"${BIN}" run --transport sctp --seed 42 --window 8 --timeout 25ms
bad_code=$?
set -e
echo "exit code: ${bad_code}"
if [[ "${bad_code}" -ne 2 ]]; then
	echo "expected exit code 2 for invalid parameters" >&2
	exit 1
fi

echo
echo "==> Demo completed successfully"
