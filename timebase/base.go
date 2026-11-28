// Package timebase converts frame-based timecodes and media tick counts into the
// single int64 nanosecond timeline shared by every subsystem of mediarc.
//
// All conversions are exact integer arithmetic with round-half-up rounding and
// explicit overflow checks, so results are deterministic across platforms and
// runs.
package timebase

// Nanosecond is one nanosecond expressed in the shared timeline unit.
const Nanosecond int64 = 1

const billion int64 = 1_000_000_000

const stream = "timebase"
