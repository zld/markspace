#!/bin/bash
# End-to-end check: build, demo render with --verify, stats shape, exit codes.
set -uo pipefail

cd "$(dirname "$0")/.."
BIN=./build/transition_lab
DEMO=/tmp/demo.tfx
OUT=/tmp/transition-frames
LOG=/tmp/transition-lab-verify.log
fail() { echo "FAIL: $*" >&2; exit 1; }

cmake -S . -B build -DCMAKE_BUILD_TYPE=Release >/dev/null || fail "configure"
cmake --build build --parallel >/dev/null || fail "build"

rm -rf "$OUT"
"$BIN" make-demo --out "$DEMO" >/dev/null || fail "make-demo exit code"
[ -s "$DEMO" ] || fail "demo manifest not written"

"$BIN" render --manifest "$DEMO" --out "$OUT" --verify >"$LOG" 2>&1
[ $? -eq 0 ] || { cat "$LOG"; fail "render --verify exit code"; }

# Per-transition stats: frames=24, 16-hex hashes, bounded middle metrics.
for k in crossfade wipe radial; do
  grep -Eq "transition [0-9]+ $k frames=24 first_hash=[0-9a-f]{16} last_hash=[0-9a-f]{16}" "$LOG" \
    || fail "missing/invalid stats line for $k"
done
grep -q "verify: ok" "$LOG" || fail "verify did not pass"

# changed_pixels within [0, 3072], mae within [0, 255].
awk '/middle_changed_pixels=/ {
  cp = $2; sub("middle_changed_pixels=", "", cp); cp += 0;
  m = $3; sub("middle_mae=", "", m); m += 0;
  if (cp < 0 || cp > 3072 || m < 0 || m > 255) { print "bad metrics: " $0 > "/dev/stderr"; exit 1 }
}' "$LOG" || fail "middle metrics out of range"

# Frame count on disk matches the rendered total.
n=$(ls "$OUT"/frame_*.ppm 2>/dev/null | wc -l | tr -d ' ')
[ "$n" = "120" ] || fail "expected 120 ppm frames, got $n"

# Exit code 2: manifest syntax error (with line number).
printf 'canvas 64 48\nclip gradient 48\nbogus line\n' >/tmp/bad-syntax.tfx
"$BIN" render --manifest /tmp/bad-syntax.tfx --out /tmp/tl-x >/tmp/tl-err.log 2>&1
[ $? -eq 2 ] || fail "syntax error should exit 2"
grep -q "line 3" /tmp/tl-err.log || fail "syntax error should report line number"

# Exit code 3: transition overlapping beyond a clip.
printf 'canvas 64 48\nclip gradient 10\ntransition wipe 20\nclip checker 48\n' >/tmp/bad-timeline.tfx
"$BIN" render --manifest /tmp/bad-timeline.tfx --out /tmp/tl-x >/dev/null 2>&1
[ $? -eq 3 ] || fail "illegal timeline should exit 3"

# Exit code 3: overlapping speed segments.
printf 'canvas 64 48\nclip gradient 48\nspeed 0 24 2/1\nspeed 12 30 1/1\nclip checker 48\ntransition crossfade 24\nclip shapes 48\n' >/tmp/bad-speed.tfx
# note: transition after second clip only; fix order for a valid parse
printf 'canvas 64 48\nclip gradient 48\nspeed 0 24 2/1\nspeed 12 30 1/1\ntransition crossfade 24\nclip checker 48\n' >/tmp/bad-speed.tfx
"$BIN" render --manifest /tmp/bad-speed.tfx --out /tmp/tl-x >/dev/null 2>&1
[ $? -eq 3 ] || fail "overlapping speed segments should exit 3"

# Exit code 2: path traversal in output path rejected.
"$BIN" render --manifest "$DEMO" --out /tmp/../tmp/tl-escape >/dev/null 2>&1
[ $? -eq 2 ] || fail "path traversal should exit 2"

# Exit code 4: unreadable manifest.
"$BIN" render --manifest /tmp/does-not-exist.tfx --out /tmp/tl-x >/dev/null 2>&1
[ $? -eq 4 ] || fail "missing manifest should exit 4"

# Determinism: a second render into another dir yields identical hashes.
"$BIN" render --manifest "$DEMO" --out /tmp/transition-frames-2 >"$LOG.2" 2>&1
grep -E "first_hash|last_hash" "$LOG" > /tmp/tl-h1
grep -E "first_hash|last_hash" "$LOG.2" > /tmp/tl-h2
diff -q /tmp/tl-h1 /tmp/tl-h2 >/dev/null || fail "repeated render hashes differ"

echo "verify.sh: all checks passed"
