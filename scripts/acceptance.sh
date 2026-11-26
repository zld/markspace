#!/usr/bin/env bash
# End-to-end local acceptance checks from the task specification.
set -euo pipefail

BREW_GO="$(brew --prefix go@1.23 2>/dev/null || true)"
if [[ -n "${BREW_GO}" && -x "${BREW_GO}/bin/go" ]]; then
  export PATH="${BREW_GO}/bin:${PATH}"
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${TMPDIR:-/tmp}/assetpack-acceptance-$$"
BIN="${WORK}/assetpack"
PKG="${WORK}/assetpack-demo"
mkdir -p "${WORK}"
cd "${REPO_ROOT}"

echo "== toolchain/build/test =="
go version
go mod tidy
go build ./...
go test ./...
go build -o "${BIN}" ./cmd/assetpack

expect_code() {
  local want="$1"; shift
  set +e
  "$@" >"${WORK}/expected.out" 2>"${WORK}/expected.err"
  local code=$?
  set -e
  if [[ ${code} -ne ${want} ]]; then
    cat "${WORK}/expected.out" "${WORK}/expected.err" >&2
    echo "expected exit ${want}, got ${code}: $*" >&2
    exit 1
  fi
}

echo "== sequence 1 normal publication =="
"${BIN}" demo --out "${PKG}" --sequence 1 --range 45000:135000 \
  --queue-blocks 2 --slow-consumer-ms 5 | tee "${WORK}/seq1.out"
grep -q 'desktop_frames=24' "${WORK}/seq1.out"
grep -q 'mobile_frames=12' "${WORK}/seq1.out"
grep -q 'audio_sample_frames=48000' "${WORK}/seq1.out"
grep -q 'audio_peak_dbfs=-12.04' "${WORK}/seq1.out"
grep -q '对齐误差: 0/1 sample' "${WORK}/seq1.out"
grep -q 'queue_peak=2 queue_capacity=2' "${WORK}/seq1.out"

cli_hash="$(sed -n 's/.*sha256=\([0-9a-f]\{64\}\).*/\1/p' "${WORK}/seq1.out" | head -1)"
disk_hash="$(shasum -a 256 "${PKG}/desktop/frame-00012.rgba" | awk '{print $1}')"
[[ "${cli_hash}" == "${disk_hash}" ]]
[[ ${#cli_hash} -eq 64 ]]

"${BIN}" demo --out "${PKG}" --sequence 1 --range 45000:135000 \
  --queue-blocks 2 --slow-consumer-ms 5 >"${WORK}/seq1-rerun.out"
rerun_hash="$(sed -n 's/.*frame_sha256.*sha256=\([0-9a-f]\{64\}\).*/\1/p' "${WORK}/seq1-rerun.out")"
[[ "${rerun_hash}" == "${cli_hash}" ]]
"${BIN}" verify --root "${PKG}" | grep -q 'pixel_check=recomputed_match'

echo "== sequence 2 and bad mdat =="
"${BIN}" demo --out "${PKG}" --sequence 2 >"${WORK}/seq2.out"
seq2_hash="$(sed -n 's/.*frame_sha256.*sha256=\([0-9a-f]\{64\}\).*/\1/p' "${WORK}/seq2.out")"
[[ "${seq2_hash}" != "${cli_hash}" ]]
before="$(readlink "${PKG}/current")"
expect_code 2 "${BIN}" demo --out "${PKG}" --sequence 2 --fault bad-mdat
after="$(readlink "${PKG}/current")"
[[ "${before}" == "${after}" ]]

echo "== rollback =="
"${BIN}" rollback --root "${PKG}" --to previous --verify | grep -q 'sequence=1'
rolled_hash="$(shasum -a 256 "${PKG}/desktop/frame-00012.rgba" | awk '{print $1}')"
[[ "${rolled_hash}" == "${cli_hash}" ]]

echo "== range and exit codes =="
expect_code 2 "${BIN}" range --root "${PKG}" --from 180000 --to 180001
expect_code 3 "${BIN}" demo --out "${WORK}/cancel" --slow-consumer-ms 50 --cancel-after 5ms
expect_code 4 "${BIN}" demo --out "${WORK}/publish-fail" --fault publish
expect_code 4 "${BIN}" rollback --root "${WORK}/missing-root" --to previous

echo "ACCEPTANCE_OK ${PKG}"
