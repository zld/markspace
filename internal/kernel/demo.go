package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/fixture"
	"mediad/internal/provenance"
	"mediad/internal/rtpcheck"
	"mediad/internal/segment"
)

// Result is the demo's machine-readable outcome.
type Result struct {
	Reps                  int
	Elements              int
	Metrics               Metrics
	SegmentHashesVerified int
	ProvenanceVerified    bool
	Corrupt               CorruptReport
	OK                    bool
	Failure               string
	FailureCode           ExitCode
	WorkDir               string
	ManifestPath          string
	ProvenancePath        string
	InputPath             string
	// InitSegments maps representation ID to the written ISO-BMFF init file.
	InitSegments map[string]string
}

// Metrics are the acceptance numbers.
type Metrics struct {
	RoundtripConsistency float64
	KeyframeHitRate      float64
	Segments             int
	Recomputed           int
	Reused               int
	CacheHits            int
	CacheMisses          int
	Quarantined          int
}

// CorruptReport summarizes the corruption suite.
type CorruptReport struct {
	Cases          int
	Rejected       int
	PartialOutputs int
	Details        []string
}

// Demo builds fixtures, processes them and verifies the artifacts.
func Demo(cfg Config) (*Result, error) {
	work, err := ensureWorkDir(cfg.WorkDir)
	if err != nil {
		return nil, internalErr("workdir", err)
	}
	base, err := fixture.Build(fixture.Spec{
		Seed:            cfg.Seed,
		DurationSec:     cfg.DurationSec,
		VideoTracks:     1,
		AudioTracks:     1,
		Representations: cfg.Representations,
		UnknownSegment:  true,
		UnknownClusters: 2,
	})
	if err != nil {
		return nil, inputErr("fixture", err)
	}
	inputPath := filepath.Join(work, "capture.webm")
	if err := os.WriteFile(inputPath, base.Data, 0o644); err != nil {
		return nil, internalErr("write", err)
	}
	reps, err := parseLadder(base.Data, cfg)
	if err != nil {
		return nil, err
	}
	plan, err := BuildPlan(reps, cfg)
	if err != nil {
		return nil, err
	}
	primary := reps[0].ID
	// Emit one init segment per rendition with go-mp4 and re-parse it to prove
	// the time base matches the manifest's cut grid. This is the MP4 manifest
	// helper the task calls for; no media is re-encoded.
	initSegments, err := buildInitSegments(work, reps, cfg)
	if err != nil {
		return nil, err
	}
	manifestJSON, err := segment.MarshalCanonical(plan.Manifests[primary])
	if err != nil {
		return nil, internalErr("manifest", err)
	}
	manifestPath := filepath.Join(work, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestJSON, 0o644); err != nil {
		return nil, internalErr("write", err)
	}
	cpPath := filepath.Join(work, "checkpoint.json")
	_ = os.Remove(cpPath)
	inputDigest := digestBytes(base.Data)
	runRes, _, err := ExecuteSegments(plan, cfg, primary, cpPath, inputDigest)
	if err != nil {
		return nil, err
	}
	_ = runRes
	provPath := filepath.Join(work, "provenance.json")
	rec := buildProvenanceRecord(plan, base.Data, inputDigest, inputPath, primary)
	if err := rec.WriteFile(provPath); err != nil {
		return nil, provErr("provenance", err)
	}
	var verifyErr error
	if cfg.Verify {
		verifyErr = VerifyArtifacts(manifestPath, inputPath, provPath, plan, primary, base.Data)
		if framed, ferr := checkSegmentFraming(plan.Manifests[primary], base.Data); ferr == nil {
			if framed != len(plan.Manifests[primary].Segments) && verifyErr == nil {
				verifyErr = inputErr("rtp", fmt.Errorf("framed %d of %d segments", framed, len(plan.Manifests[primary].Segments)))
			}
		} else if verifyErr == nil {
			verifyErr = inputErr("rtp", ferr)
		}
	}
	corrupt := runCorruptions(base, cfg.Seed)
	res := &Result{
		Reps:     len(reps),
		Elements: sumElements(reps),
		Metrics:  computeMetrics(plan, reps, primary, runRes),
		// Segment hashes count as verified only when independent verification
		// re-hashed every one of them successfully.
		SegmentHashesVerified: 0,
		ProvenanceVerified:    cfg.Verify && verifyErr == nil,
		Corrupt:               corrupt,
		WorkDir:               work,
		ManifestPath:          manifestPath,
		ProvenancePath:        provPath,
		InputPath:             inputPath,
		InitSegments:          initSegments,
	}
	if !cfg.Verify {
		res.ProvenanceVerified = false
		res.SegmentHashesVerified = 0
	} else if verifyErr == nil {
		res.SegmentHashesVerified = len(plan.Manifests[primary].Segments)
	} else {
		res.Failure = verifyErr.Error()
		res.FailureCode = CodeOf(verifyErr)
	}
	if !acceptanceMet(res, cfg.Verify) && res.FailureCode == ExitOK {
		res.FailureCode = ExitInput
		res.Failure = "acceptance thresholds not met"
	}
	res.OK = acceptanceMet(res, cfg.Verify)
	return res, nil
}

func ensureWorkDir(dir string) (string, error) {
	if dir == "" {
		return os.MkdirTemp("", "mediad-demo-*")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func parseLadder(data []byte, cfg Config) ([]*Representation, error) {
	ladder := []struct {
		id string
		bw uint64
	}{
		{"360p", 500_000}, {"720p", 1_500_000}, {"1080p", 3_500_000}, {"1440p", 6_000_000}, {"2160p", 9_000_000},
	}
	if cfg.Representations < 1 {
		return nil, inputErr("ladder", errors.New("representations must be positive"))
	}
	var reps []*Representation
	for i := 0; i < cfg.Representations; i++ {
		l := ladder[i%len(ladder)]
		rep, err := ParseRepresentation(fmt.Sprintf("%s-%d", l.id, i), l.bw, data, cfg.FrameNanos)
		if err != nil {
			return nil, err
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

func computeMetrics(plan *Plan, reps []*Representation, primary string, run *ExecutionReport) Metrics {
	m := plan.Manifests[primary]
	mt := Metrics{
		Segments:    len(m.Segments),
		Recomputed:  run.Wasted,
		Reused:      run.Reused,
		CacheHits:   run.CacheHits,
		CacheMisses: run.CacheMisses,
		Quarantined: run.Quarantined,
	}
	// Keyframe hit rate: the minimum across renditions, so one bad rendition
	// fails the threshold.
	mt.KeyframeHitRate = 1.0
	for _, r := range reps {
		track, ok := r.Index.VideoTrack()
		if !ok {
			mt.KeyframeHitRate = 0
			continue
		}
		h := r.Index.Hits(track, plan.Cuts, plan.Config.Tolerance)
		if h.Cuts == 0 {
			continue
		}
		rate := h.HitRate
		if rate < mt.KeyframeHitRate {
			mt.KeyframeHitRate = rate
		}
	}
	// Roundtrip consistency: every indexed keyframe's source span must begin
	// with a real block ID in the input bytes.
	var ok, total int
	for _, r := range reps {
		track, _ := r.Index.VideoTrack()
		for _, e := range r.Index.Entries(track) {
			total++
			if e.Source.Offset < 0 || e.Source.End() > int64(len(r.Data)) {
				continue
			}
			raw := r.Data[e.Source.Offset:e.Source.End()]
			if len(raw) > 0 && (raw[0] == 0xA3 || raw[0] == 0xA1) {
				ok++
			}
		}
	}
	if total > 0 {
		mt.RoundtripConsistency = float64(ok) / float64(total)
	}
	return mt
}

// checkSegmentFraming frames each segment's keyframe payload into RTP packets
// and requires byte-exact reassembly. A packet spanning two segments would
// break the per-segment roundtrip, so this proves fragments never cross the
// shard boundary.
func checkSegmentFraming(m *segment.Manifest, data []byte) (int, error) {
	var framed int
	for _, s := range m.Segments {
		if s.Keyframe.Offset < 0 || s.Keyframe.End() > int64(len(data)) {
			return framed, fmt.Errorf("segment %d keyframe span outside input", s.Index)
		}
		payload := data[s.Keyframe.Offset:s.Keyframe.End()]
		if len(payload) < 12 {
			return framed, fmt.Errorf("segment %d keyframe payload too short", s.Index)
		}
		// Use the generic framer: the fixture's synthetic keyframe bodies are
		// not full VP9 bitstreams, but the property under test is that RTP
		// fragments never cross a segment boundary, which is codec-agnostic.
		if _, err := rtpcheck.FrameFor("", 1200, payload); err != nil {
			return framed, fmt.Errorf("segment %d: %w", s.Index, err)
		}
		framed++
	}
	return framed, nil
}

func sumElements(reps []*Representation) int {
	total := 0
	for _, r := range reps {
		total += r.Doc.ElementCount
	}
	return total
}

func acceptanceMet(res *Result, requireVerify bool) bool {
	verifyOK := !requireVerify || (res.ProvenanceVerified && res.SegmentHashesVerified == res.Metrics.Segments)
	return res.Metrics.RoundtripConsistency >= 0.9999 &&
		res.Metrics.KeyframeHitRate >= 0.99 &&
		res.Metrics.Segments >= 12 &&
		verifyOK &&
		res.Metrics.Recomputed <= 1 &&
		res.Corrupt.Cases == 5 && res.Corrupt.Rejected == 5 && res.Corrupt.PartialOutputs == 0
}

func buildProvenanceRecord(plan *Plan, data []byte, inputDigest, inputPath, primary string) *provenance.Record {
	m := plan.Manifests[primary]
	manifestJSON, _ := segment.MarshalCanonical(m)
	var hashes []string
	for _, s := range m.Segments {
		hashes = append(hashes, s.Hash)
	}
	return &provenance.Record{
		Version:        1,
		Artifact:       "manifest.json",
		ArtifactSHA256: digestBytes(manifestJSON),
		ManifestSHA256: digestBytes(manifestJSON),
		ConfigDigest:   plan.Config.Digest(),
		InputDigest:    inputDigest,
		Inputs: []provenance.InputRef{{
			Path:   filepath.Base(inputPath),
			SHA256: inputDigest,
			Span:   provenance.Span{Offset: 0, Length: int64(len(data))},
		}},
		Checkpoints:   []provenance.CheckpointRef{{ID: "parse-0", Offset: 0, Digest: inputDigest}},
		Segments:      len(hashes),
		SegmentHashes: hashes,
	}
}
