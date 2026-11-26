// Package verify independently re-parses every published artifact. It never
// receives the planner's accept decision: it re-opens the init segment, every
// media segment, the manifest and the RTP audit trail, then checks SSIM, peak
// bitrate, boundary continuity, round-trip parsing and content hashes.
package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/fault"
	"regress/internal/publish"
	"regress/internal/worker"
)

// Expectations are the constraints the verifier independently enforces.
type Expectations struct {
	SSIMMin       float64
	MaxBitrateBPS int64
	// MeasuredSSIM is recomputed externally and passed in; the verifier still
	// owns the threshold decision.
	MeasuredSSIM float64
}

// Report is the independent verification verdict.
type Report struct {
	SegmentsChecked int
	HashesOK        int
	RoundTripPct    float64
	SSIMMin         float64
	BitratePeakBPS  int64
	BoundaryOK      bool
	AuditOK         bool
	Failures        []string
}

// Checker re-parses a published representation rooted at the directory that
// contains manifest.json.
type Checker struct {
	pubRoot string
}

// NewChecker roots verification at a publish job directory.
func NewChecker(pubRoot string) *Checker { return &Checker{pubRoot: pubRoot} }

// safeJoin resolves a manifest-supplied file name strictly inside the job
// root. Any absolute path or ".." traversal that would escape the root is
// rejected with a structured I/O fault rather than followed.
func safeJoin(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fault.New(fault.StageVerify, fault.CodeIO, "",
			"manifest entry must be a relative file name: "+name)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(cleanRoot, filepath.Clean("/"+name))
	rel, err := filepath.Rel(cleanRoot, joined)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return "", fault.New(fault.StageVerify, fault.CodeIO, "",
			"manifest path escapes job root: "+name)
	}
	return joined, nil
}

// Check performs every independent check. A non-nil error carries structured
// fault codes for the first hard failure; the Report accumulates all findings.
func (c *Checker) Check(ctx context.Context, exp Expectations) (*Report, error) {
	manifestPath := filepath.Join(c.pubRoot, "manifest.json")
	manifest, err := publish.ReadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	rep := &Report{SSIMMin: exp.MeasuredSSIM, BoundaryOK: true}

	// 1. Init segment hash and independent re-parse.
	initPath, err := safeJoin(c.pubRoot, manifest.InitFile)
	if err != nil {
		return rep, err
	}
	if err := worker.VerifyHash(initPath, manifest.InitSHA256); err != nil {
		return rep, err
	}
	initMeta, err := publish.ParseInit(initPath)
	if err != nil {
		return rep, err
	}
	if initMeta.Timescale != manifest.Timescale {
		return rep, fault.New(fault.StageVerify, fault.CodeManifestInconsistent, manifest.Codec,
			fmt.Sprintf("timescale mismatch: manifest=%d parsed=%d", manifest.Timescale, initMeta.Timescale))
	}

	// 2. Re-parse every media segment independently and compare to manifest.
	var prevEnd uint64
	var peak int64
	for i, entry := range manifest.Segments {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		segPath, err := safeJoin(c.pubRoot, entry.File)
		if err != nil {
			return rep, err
		}
		if _, err := os.Stat(segPath); err != nil {
			return rep, fault.New(fault.StageVerify, fault.CodeIO, manifest.Codec,
				"manifest references missing segment: "+entry.File).Wrap(err)
		}
		// Hash check.
		if err := worker.VerifyHash(segPath, entry.SHA256); err != nil {
			return rep, err
		}
		rep.HashesOK++
		meta, err := publish.ParseSegment(segPath)
		if err != nil {
			return rep, err
		}
		meta.Timescale = initMeta.Timescale
		rep.SegmentsChecked++

		// Manifest must match the freshly parsed box values.
		if meta.Sequence != entry.Sequence ||
			meta.BaseTime != entry.BaseTimeTicks ||
			meta.DurationTicks != entry.DurationTicks ||
			meta.SampleCount != entry.SampleCount {
			rep.BoundaryOK = false
			rep.Failures = append(rep.Failures, fmt.Sprintf(
				"segment %d manifest/parse drift: seq %d/%d base %d/%d dur %d/%d",
				i, entry.Sequence, meta.Sequence, entry.BaseTimeTicks, meta.BaseTime,
				entry.DurationTicks, meta.DurationTicks))
		}
		// Boundary continuity: base must equal the previous segment's end.
		if i > 0 && meta.BaseTime != prevEnd {
			rep.BoundaryOK = false
			rep.Failures = append(rep.Failures, fmt.Sprintf(
				"segment %d boundary discontinuity base=%d expected=%d", i, meta.BaseTime, prevEnd))
		}
		prevEnd = meta.BaseTime + meta.DurationTicks

		// Independent peak bitrate from parsed duration and on-disk size.
		info, err := os.Stat(segPath)
		if err != nil {
			return rep, fault.New(fault.StageVerify, fault.CodeIO, manifest.Codec, err.Error()).Wrap(err)
		}
		durSecs := float64(meta.DurationTicks) / float64(meta.Timescale)
		if durSecs > 0 {
			bps := int64(float64(info.Size()*8) / durSecs)
			if bps > peak {
				peak = bps
			}
		}
	}
	rep.BitratePeakBPS = peak

	// 3. RTP audit trail round trip and timing re-derivation.
	trail, err := publish.ReadAuditTrail(filepath.Join(c.pubRoot, "rtp-audit.json"))
	if err != nil {
		return rep, err
	}
	metas := make([]*publish.SegmentMeta, 0, len(manifest.Segments))
	for _, e := range manifest.Segments {
		metas = append(metas, &publish.SegmentMeta{
			Sequence: e.Sequence, BaseTime: e.BaseTimeTicks,
			DurationTicks: e.DurationTicks, Timescale: manifest.Timescale,
		})
	}
	if err := publish.VerifyAuditTrail(trail, metas); err != nil {
		rep.AuditOK = false
		rep.Failures = append(rep.Failures, "rtp audit verification failed: "+err.Error())
	} else {
		rep.AuditOK = true
	}

	// 4. Round-trip coverage: every manifest entry was successfully re-parsed.
	if len(manifest.Segments) > 0 {
		rep.RoundTripPct = 100 * float64(rep.SegmentsChecked) / float64(len(manifest.Segments))
	}

	// 5. Independent threshold decisions.
	var hard error
	if exp.MeasuredSSIM < exp.SSIMMin {
		rep.Failures = append(rep.Failures, fmt.Sprintf(
			"ssim %.4f below minimum %.4f", exp.MeasuredSSIM, exp.SSIMMin))
		hard = fault.New(fault.StageVerify, fault.CodeQualityBelowMin, manifest.Codec,
			"independent SSIM check failed")
	}
	if peak > exp.MaxBitrateBPS {
		rep.Failures = append(rep.Failures, fmt.Sprintf(
			"peak bitrate %d exceeds ceiling %d", peak, exp.MaxBitrateBPS))
		hard = fault.New(fault.StageVerify, fault.CodeBitrateAboveMax, manifest.Codec,
			"independent bitrate check failed")
	}
	if !rep.BoundaryOK {
		hard = fault.New(fault.StageVerify, fault.CodeBoundaryDiscontinuity, manifest.Codec,
			"independent boundary check failed")
	}
	return rep, hard
}
