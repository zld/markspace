#!/bin/sh
# Acceptance entry: build, test, and run the full verify path.
# Usage: scripts/acceptance.sh
set -eu
cd "$(dirname "$0")/.."

go mod tidy
go build ./...
go test ./...

OUT=$(go run ./cmd/mediad verify --generate-fixtures --work ./tmp/acceptance \
	--budget-ms 5000 --max-bytes 1048576)
echo "$OUT"

# Required normal-path metrics.
for want in \
	"roundtrip_rate=1.000" \
	"keyframe_hit_rate=1.000" \
	"segment_hash_match=1" \
	"manifest_stable=1" \
	"resumed_from_checkpoint=1" \
	"rerun_hash_match=1"; do
	echo "$OUT" | grep -q "^$want\$" || { echo "MISSING: $want"; exit 1; }
done

# Corrupt fixtures must carry stable localization.
echo "$OUT" | grep -q "^error_code=ERR_" || { echo "MISSING: error_code"; exit 1; }
echo "$OUT" | grep -q "byte_offset=" || { echo "MISSING: byte_offset"; exit 1; }
echo "$OUT" | grep -q "element_path=" || { echo "MISSING: element_path"; exit 1; }

# Boundary rejections must be at least 1.
REJ=$(echo "$OUT" | sed -n 's/^boundary_rejections=\([0-9]*\)$/\1/p')
[ "${REJ:-0}" -ge 1 ] || { echo "MISSING: boundary rejections"; exit 1; }

# Corrupt input exits 2 with the built binary (go run masks exit codes).
go build -o ./tmp/mediad ./cmd/mediad
set +e
./tmp/mediad parse --input ./tmp/acceptance/corrupt-vint.mkv >/dev/null
CODE=$?
set -e
[ "$CODE" -eq 2 ] || { echo "corrupt parse: expected exit 2, got $CODE"; exit 1; }

echo "acceptance OK"
