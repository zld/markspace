// Package clock normalizes media clocks that were captured on different
// nodes onto one shared master timeline.
//
// Three problems are handled explicitly:
//
//   - Scale. Every track declares how many seconds one native tick is worth;
//     a point's master value is Origin + Native*Scale, all in exact rationals.
//   - Drift. A per-node least-squares fit (node = a*source + b) is kept whole
//     (slope, intercept, ppb, residual, sample count) so the correction can be
//     audited rather than silently applied. Correction inverts that fit.
//   - Rollover. A 32-bit RTP timestamp is extended to 64 bits monotonically.
//     The raw value is always preserved alongside the extension, and a step is
//     classified as a wrap only when the signed 32-bit delta is positive while
//     the raw value numerically decreased, which separates rollover from a
//     genuine regression.
//
// After normalization no per-track clock may move backwards. When a drift
// correction would do so, the corrector clamps to the previous value and
// counts the clamp, and the pipeline reports it as a non-monotonic finding at
// the offending input span.
package clock
