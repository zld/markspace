// Package validate holds the invariant checks that gate a report.
//
// Two levels exist. The per-stage functions (IngestStage, NormalizeStage,
// IndexStage, DiagnoseStage) check a stage's own output: finding and cue spans
// inside the asset, a monotonic normalized clock, keyframe references that
// resolve and agree with their offsets, and corrupt ranges that are half-open,
// ordered and in bounds. All() repeats those checks over the whole run,
// together with the drift audit and the asset-format check, for the final
// report and for `verify`.
//
// Checks are sorted by (name, asset, start) and carry no timing data, so the
// same inputs always produce the same check list.
package validate
