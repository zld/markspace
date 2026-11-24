#!/usr/bin/env bash
# macOS local acceptance check for mediarc.
# It runs the exact commands from the delivery contract and additionally checks
# raw process exit codes with a compiled binary (`go run` wraps child exits).
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
go test ./...

rm -rf ./tmp/accept ./tmp/exit-sync ./tmp/exit-channel
mkdir -p ./tmp
go build -o ./tmp/mediarc ./cmd/mediarc

echo "== demo =="
go run ./cmd/mediarc demo --seed 17 --frames 120 --audio-packets 480 --chunk 16 \
  --inject bad-sha,truncated,late,duplicate --workdir ./tmp/accept | tee ./tmp/demo.out

for pattern in \
  '^frame_total=120$' \
  '^bad_sha=1$' \
  '^truncated=1$' \
  '^late_packets=[1-9][0-9]*$' \
  '^duplicate_packets=[1-9][0-9]*$' \
  '^定位误差=0$' \
  '^chunk_count=8$'; do
  grep -Eq "$pattern" ./tmp/demo.out || { echo "missing output: $pattern" >&2; exit 1; }
done

echo "== first interrupted resume (application exit_code=10) =="
set +e
go run ./cmd/mediarc resume --workdir ./tmp/accept --interrupt-after 3 2>&1 | tee ./tmp/resume-1.out
# The go tool reports the child's 10 as "exit status 10" and its own status is
# 1; the application summary carries the stable exit_code=10 contract.
set -e
grep -Eq '^committed_chunks=3$' ./tmp/resume-1.out
grep -Eq '^exit_code=10$' ./tmp/resume-1.out

echo "== resume from fourth frame chunk =="
go run ./cmd/mediarc resume --workdir ./tmp/accept | tee ./tmp/resume-2.out
grep -Eq '^resumed_from_chunk=3$' ./tmp/resume-2.out
grep -Eq '^resume_elapsed_ms=[0-9]+$' ./tmp/resume-2.out
grep -Eq '^恢复耗时毫秒=[0-9]+$' ./tmp/resume-2.out
sha1=$(grep -E '^sha256=[0-9a-f]{64}$' ./tmp/resume-2.out | head -1 | cut -d= -f2)
[[ ${#sha1} -eq 64 ]]

echo "== repeated run is deterministic =="
go run ./cmd/mediarc resume --workdir ./tmp/accept | tee ./tmp/resume-3.out
sha2=$(grep -E '^sha256=[0-9a-f]{64}$' ./tmp/resume-3.out | head -1 | cut -d= -f2)
[[ "$sha1" == "$sha2" ]]
shasum -a 256 ./tmp/accept/recovered.mp4 | awk -v want="$sha1" '$1 != want {exit 1} $1 == want {print "artifact sha256="$1}'

echo "== exit code 2: corrupt input =="
set +e
./tmp/mediarc validate --workdir ./tmp/accept >/tmp/mediarc-validate.out 2>&1
code=$?
set -e
[[ $code -eq 2 ]]

echo "== exit code 3: A/V sync =="
./tmp/mediarc demo --seed 17 --frames 24 --audio-packets 64 --chunk 8 \
  --inject bad-sha,late,duplicate --workdir ./tmp/exit-sync >/dev/null
set +e
./tmp/mediarc resume --workdir ./tmp/exit-sync --av-offset-ms 50 >/tmp/mediarc-sync.out 2>&1
code=$?
set -e
[[ $code -eq 3 ]]
grep -Eq '^exit_code=3$' /tmp/mediarc-sync.out

echo "== exit code 3: unknown channel layout =="
./tmp/mediarc demo --seed 17 --frames 24 --audio-packets 64 --chunk 8 \
  --workdir ./tmp/exit-channel >/dev/null
set +e
./tmp/mediarc resume --workdir ./tmp/exit-channel --source-layout mystery-9.1 >/tmp/mediarc-channel.out 2>&1
code=$?
set -e
[[ $code -eq 3 ]]

echo "ACCEPTANCE_OK sha256=$sha1"
