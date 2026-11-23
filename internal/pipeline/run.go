// Package pipeline orchestrates the stages: ingest, clock alignment, metadata
// transform, cutting and silence analysis, fingerprint dedup, then manifest
// validation. Recovery mode skips only unparseable units and keeps producing
// a manifest; repeated runs are byte-identical.
package pipeline

import (
	"mediapipe/internal/audio"
	"mediapipe/internal/clock"
	"mediapipe/internal/fault"
	"mediapipe/internal/fingerprint"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
	"mediapipe/internal/testdata"
	"mediapipe/internal/transform"
)

// Options controls a demo pipeline run.
type Options struct {
	WorkDir     string
	Seed        int64
	TargetKbps  int
	DriftPPM    float64
	WrapRTP     bool
	VFR         bool
	Corruption  testdata.Corruption
	Recover     bool
	ClockRate   uint32
}

// Result bundles outputs the CLI adapter reports.
type Result struct {
	Timeline   *model.Timeline
	Manifest   manifest.Manifest
	ManifestBytes []byte
	Stats      manifest.Stats
	Errors     *fault.Batch
	Normalized map[string]transform.Normalized
	Silence    map[string]audio.Result
}

// Run executes every stage. A non-nil Result with recoverable faults is
// returned even when the input is damaged; fatal faults are returned via the
// error value for exit-code mapping.
func Run(opts Options) (*Result, error) {
	errs := &fault.Batch{}
	files := buildFixtures(opts, errs)
	if len(errs.Fatal()) > 0 {
		return nil, errs.Fatal()[0]
	}

	tl := &model.Timeline{SessionID: "session-demo"}
	ingestAll(tl, files, opts, errs)
	if len(errs.Fatal()) > 0 && !opts.Recover {
		return &Result{Timeline: tl, Errors: errs}, errs.Fatal()[0]
	}

	alignRes := clock.Align(tl, errs)
	normalized := transform.NormalizeAll(tl, errs)
	silenceRes := runSilence(tl)
	segments, hashes := cutAndHash(tl, silenceRes, errs)
	flatHashes := flattenHashes(hashes)
	matches := fingerprint.FindDups(flatHashes, fingerprint.DefaultHasher())

	stats := buildStats(tl, alignRes, silenceRes, segments, hashes, matches, opts, errs)
	segRecords := toSegmentRecords(segments, hashes)
	m := manifest.Build(tl.SessionID, tl, segRecords, stats)
	manifest.Validate(m, errs)
	body, hash, err := manifest.Seal(m)
	if err != nil {
		return nil, err
	}
	m.ContentHash = hash
	return finalize(tl, m, body, stats, errs, normalized, silenceRes)
}
