#!/usr/bin/env sh
# Reproducible offline demo of edgewatch. Works on Linux and macOS; on Windows
# use the equivalent `cargo run --release -- ...` command from PowerShell.
set -e

cargo run --release -- --demo --window-secs 60 --lateness-secs 15
