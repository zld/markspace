#!/usr/bin/env sh
# Regenerate the deterministic sample fixtures into testdata/.
# Every fixture is produced in-process by the Go code; no external media.
set -eu
cd "$(dirname "$0")/.."
mkdir -p testdata
cat > /tmp/mediad_gen_$$.go <<'EOF'
package main

import (
	"os"

	"mediad/internal/sample"
)

func main() {
	out := os.Args[1]
	for _, kind := range sample.AllKinds() {
		data, _ := sample.Generate(kind)
		_ = os.WriteFile(out+"/"+string(kind)+".ts", data, 0o644)
	}
	trunc, _ := sample.Truncate()
	_ = os.WriteFile(out+"/truncated.ts", trunc, 0o644)
}
EOF
mkdir -p cmd/genfixtures
mv /tmp/mediad_gen_$$.go cmd/genfixtures/main.go
go run ./cmd/genfixtures testdata
ls -1 testdata
