#!/bin/zsh
# Reproduces the full lutgrade acceptance demo from a clean terminal.
# Requires: cmake 3.29+, Apple Clang (xcode-select --install).
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== configure (Release) =="
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release

echo "== build =="
cmake --build build --parallel

echo "== ctest =="
ctest --test-dir build --output-on-failure

echo "== generate deterministic fixtures =="
./build/lutgrade fixture --dir build/demo --width 96 --height 64 --grid 17

echo "== render: sRGB input, Display-P3 LUT, sRGB output, compare paths =="
./build/lutgrade render \
    --input build/demo/input.ppm \
    --lut build/demo/look.cube \
    --input-space srgb \
    --lut-space display-p3 \
    --output-space srgb \
    --interpolation tetrahedral \
    --path compare \
    --threads 4 \
    --output build/demo/graded.ppm

echo "== repeatability check: rerun and diff the outputs =="
./build/lutgrade render \
    --input build/demo/input.ppm \
    --lut build/demo/look.cube \
    --input-space srgb \
    --lut-space display-p3 \
    --output-space srgb \
    --interpolation tetrahedral \
    --threads 4 \
    --output build/demo/graded_again.ppm
if cmp -s build/demo/graded.ppm build/demo/graded_again.ppm; then
    echo "deterministic: graded.ppm and graded_again.ppm are byte-identical"
else
    echo "ERROR: rerun produced different bytes" >&2
    exit 1
fi

echo "== demo complete =="
