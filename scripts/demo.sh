#!/usr/bin/env bash
# demo.sh - build lutcheck and run the end-to-end acceptance demo:
# grading consistency demo, self-test, and corrupted-LUT handling.
set -euo pipefail

cd "$(dirname "$0")/.."

cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel

echo "== demo: display-p3, 17^3 LUT, 4099 pixels, 16 bins, block 257 =="
./build/lutcheck demo --space display-p3 --size 17 --pixels 4099 \
  --bins 16 --block-size 257 --json

echo "== self-test =="
./build/lutcheck self-test

echo "== corrupted LUT demo (expect exit code 3) =="
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

# Too few data rows for LUT_3D_SIZE 2 (needs 8 rows).
cat > "$tmp_dir/truncated.cube" <<'EOF'
TITLE "truncated"
LUT_3D_SIZE 2
0 0 0
0 0 1
EOF
# Inverted domain range.
cat > "$tmp_dir/bad_domain.cube" <<'EOF'
LUT_3D_SIZE 2
DOMAIN_MIN 1 0 0
DOMAIN_MAX 0 1 1
0 0 0
0 0 1
0 1 0
0 1 1
1 0 0
1 0 1
1 1 0
1 1 1
EOF

for bad in truncated bad_domain; do
  if ./build/lutcheck run --lut "$tmp_dir/$bad.cube" --pixels 64; then
    echo "ERROR: corrupted LUT '$bad' was accepted" >&2
    exit 1
  else
    rc=$?
    if [ "$rc" -ne 3 ]; then
      echo "ERROR: corrupted LUT '$bad' exited $rc, expected 3" >&2
      exit 1
    fi
    echo "corrupted LUT '$bad' rejected with exit code 3 (expected)"
  fi
done

echo "== argument error demo (expect exit code 2) =="
if ./build/lutcheck demo --block-size 0; then
  echo "ERROR: --block-size 0 was accepted" >&2
  exit 1
else
  rc=$?
  [ "$rc" -eq 2 ] || { echo "ERROR: exited $rc, expected 2" >&2; exit 1; }
  echo "--block-size 0 rejected with exit code 2 (expected)"
fi

echo "demo=passed"
