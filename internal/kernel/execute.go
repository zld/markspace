package kernel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediad/internal/pipeline"
	"mediad/internal/provenance"
	"mediad/internal/segment"
)

// ExecutionReport summarizes a checkpointed execution.
type ExecutionReport struct {
	// Recomputed is the number of segments actually executed after resume.
	Recomputed int
	// Reused is the number of segments resumed from the checkpoint.
	Reused int
	// Wasted counts work executed for segment indices that were already
	// committed before the resume. It must stay at zero: resume may only
	// recompute unfinished work.
	Wasted int
	// RTPFramed is the number of segments whose video payload framed into RTP
	// packets without crossing the segment boundary.
	RTPFramed int
}

// ExecuteSegments writes manifests through the checkpointed runner, simulating
// an interruption after cfg.ResumeAfter segments.
func ExecuteSegments(plan *Plan, cfg Config, primary, cpPath, inputDigest string) (*ExecutionReport, *pipeline.Checkpoint, error) {
	m := plan.Manifests[primary]
	if m == nil {
		return nil, nil, internalErr("execute", fmt.Errorf("no manifest for %s", primary))
	}
	if err := os.MkdirAll(filepath.Dir(cpPath), 0o755); err != nil {
		return nil, nil, err
	}
	cfgDigest := plan.Config.Digest()
	work := func(i int) ([]byte, string, error) {
		if i < 0 || i >= len(m.Segments) {
			return nil, "", internalErr("execute", fmt.Errorf("segment %d out of range", i))
		}
		b, err := segment.MarshalCanonical(m.Segments[i])
		return b, m.Segments[i].Hash, err
	}
	prefix := cfg.ResumeAfter
	if prefix > len(m.Segments) {
		prefix = len(m.Segments)
	}
	if prefix > 0 {
		first, err := pipeline.NewRunner(cpPath, inputDigest, cfgDigest, pipeline.RunConfig{TotalSegments: prefix})
		if err != nil {
			return nil, nil, inputErr("checkpoint", err)
		}
		if _, err := first.Run(pipeline.RunConfig{TotalSegments: prefix}, work); err != nil {
			return nil, nil, internalErr("execute", err)
		}
	}
	// Record which segments were already committed before the resume so any
	// later execution of them can be counted as wasted work.
	committed := map[int]bool{}
	if cp, loadErr := pipeline.LoadCheckpoint(cpPath, inputDigest, cfgDigest); loadErr == nil {
		for idx := range cp.CompletedSet() {
			committed[idx] = true
		}
	}
	wasted := 0
	guarded := func(i int) ([]byte, string, error) {
		if committed[i] {
			wasted++
		}
		return work(i)
	}
	recompute := func(i int) (string, error) {
		if i < 0 || i >= len(m.Segments) {
			return "", fmt.Errorf("segment %d out of range", i)
		}
		return m.Segments[i].Hash, nil
	}
	runner, err := pipeline.NewRunner(cpPath, inputDigest, cfgDigest, pipeline.RunConfig{TotalSegments: len(m.Segments), Recompute: recompute})
	if err != nil {
		return nil, nil, inputErr("checkpoint", err)
	}
	// Record how far parsing had reached so a future run can resume without
	// re-reading a prefix it has already consumed.
	runner.SetParseOffset(plan.ParseOffset)
	res, err := runner.Run(pipeline.RunConfig{TotalSegments: len(m.Segments), Recompute: recompute}, guarded)
	if err != nil {
		return nil, nil, internalErr("execute", err)
	}
	return &ExecutionReport{Recomputed: res.Recomputed, Reused: res.Reused, Wasted: wasted}, runner.Checkpoint(), nil
}

// VerifyArtifacts re-reads artifacts from disk and independently checks
// provenance plus cross-stage invariants.
func VerifyArtifacts(manifestPath, inputPath, provPath string, plan *Plan, primary string, inputData []byte) error {
	loaded, err := segment.LoadManifest(manifestPath)
	if err != nil {
		return provErr("load", err)
	}
	inMemory := plan.Manifests[primary]
	if inMemory == nil {
		return internalErr("verify", fmt.Errorf("no manifest for %s", primary))
	}
	if !sameLayout(loaded, inMemory) {
		return provErr("layout", errors.New("manifest on disk differs from in-memory manifest"))
	}
	rec, err := provenance.LoadFile(provPath)
	if err != nil {
		return provErr("provenance", err)
	}
	sums := make([]string, len(loaded.Segments))
	for i, s := range loaded.Segments {
		sums[i] = s.Hash
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return internalErr("verify", err)
	}
	exp := provenance.Expected{
		ArtifactPath:   manifestPath,
		ArtifactSHA256: digestBytes(manifestBytes),
		ManifestSHA256: digestBytes(manifestBytes),
		ConfigDigest:   plan.Config.Digest(),
		SegmentHashes:  sums,
		InputPaths: map[string]func() (io.ReadCloser, error){
			filepath.Base(inputPath): func() (io.ReadCloser, error) { return os.Open(inputPath) },
		},
	}
	if failures := provenance.Verify(rec, exp); len(failures) > 0 {
		return provErr("provenance", fmt.Errorf("%d provenance failure(s): %s", len(failures), failures[0].Error()))
	}
	if err := checkInvariants(loaded, plan, inputData); err != nil {
		return provErr("invariants", err)
	}
	return nil
}

func sameLayout(a, b *segment.Manifest) bool {
	if len(a.Segments) != len(b.Segments) || a.ConfigDigest != b.ConfigDigest || a.InputDigest != b.InputDigest {
		return false
	}
	for i := range a.Segments {
		if a.Segments[i].StartNS != b.Segments[i].StartNS || a.Segments[i].EndNS != b.Segments[i].EndNS || a.Segments[i].Hash != b.Segments[i].Hash {
			return false
		}
	}
	return true
}

// checkKeyframeBytes re-reads the block the manifest claims starts the segment
// and confirms, from the real bytes, that it is a SimpleBlock with the keyframe
// flag set. This is the "index hit corresponds to a real keyframe" invariant:
// it validates the artifact against the input rather than against the index the
// producer used.
func checkKeyframeBytes(s segment.Segment, data []byte) error {
	if s.Keyframe.Offset < 0 || s.Keyframe.End() > int64(len(data)) {
		return errors.New("keyframe span outside input")
	}
	raw := data[s.Keyframe.Offset:s.Keyframe.End()]
	if len(raw) < 6 {
		return errors.New("keyframe span too short for a SimpleBlock")
	}
	if raw[0] != 0xA3 {
		return fmt.Errorf("keyframe span starts with 0x%02x, not SimpleBlock", raw[0])
	}
	// Skip the element ID (1 byte for A3) and the size VINT.
	sizeWidth := 1
	for sizeWidth < 9 && raw[sizeWidth]&0x80 == 0 {
		sizeWidth++
	}
	if 1+sizeWidth >= len(raw) {
		return errors.New("keyframe span has no block body")
	}
	body := raw[1+sizeWidth:]
	// Track number is an EBML unsigned VINT: width is its marker position.
	trackWidth := 1
	for trackWidth <= 8 && body[0]&(0x80>>uint(trackWidth-1)) == 0 {
		trackWidth++
	}
	if trackWidth+3 > len(body) {
		return errors.New("keyframe block too short for timecode and flags")
	}
	flags := body[trackWidth+2]
	if flags&0x80 == 0 {
		return errors.New("indexed keyframe has no keyframe flag in the source bytes")
	}
	return nil
}

func checkInvariants(m *segment.Manifest, plan *Plan, data []byte) error {
	for i, s := range m.Segments {
		if s.EndNS <= s.StartNS {
			return fmt.Errorf("segment %d has non-positive duration", i)
		}
		if i > 0 {
			prev := m.Segments[i-1]
			if s.StartNS < prev.EndNS {
				return fmt.Errorf("segment %d overlaps segment %d", i, i-1)
			}
			if gap := s.StartNS - prev.EndNS; gap > plan.Config.FrameNanos {
				return fmt.Errorf("segment %d has a %d ns gap", i, gap)
			}
		}
		if err := checkKeyframeBytes(s, data); err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
	}
	return nil
}
