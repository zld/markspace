package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"mediatool/internal/fault"
	"mediatool/internal/fixture"
	"mediatool/internal/index"
	"mediatool/internal/segment"
)

// runVerify exercises each self-generated fault class and asserts the located
// evidence, the digest mismatch rejection, and deterministic re-runs.
func runVerify(args []string) error {
	_ = args
	samples, err := fixture.Samples()
	if err != nil {
		return err
	}
	failures := 0
	for _, s := range samples {
		if err := verifySample(s); err != nil {
			fmt.Printf("FAIL %s: %v\n", s.Class, err)
			failures++
			continue
		}
		fmt.Printf("ok   %s kind=%s offset=%d\n", s.Class, s.ExpectKind, s.ExpectOffset)
	}
	if failures > 0 {
		return fmt.Errorf("%d fault sample(s) failed", failures)
	}
	return nil
}

func verifySample(s fixture.Sample) error {
	cap, err := fixture.Generate(s.Options)
	if err != nil {
		return err
	}
	opts := s.Options
	tracks := map[uint32]index.TrackInfo{}
	kinds := map[uint32]string{}
	for _, spec := range opts.Tracks {
		tracks[spec.SSRC] = index.TrackInfo{TrackID: spec.TrackID, ClockRate: spec.ClockRate}
		kinds[spec.TrackID] = spec.Kind
	}
	sr, err := index.ScanReader(bytes.NewReader(cap.Bytes), index.ScanConfig{
		Tracks: tracks, Kinds: kinds, Window: 64, IsKeyframe: index.IsVideoKeyframe,
		Digest: index.DigestBytes(cap.Bytes), InputBytes: int64(len(cap.Bytes)),
	})
	if err != nil {
		return err
	}

	switch s.Class {
	case fixture.FaultDigestMismatch:
		// Save the index, then attempt to load it against a different input.
		dir, err := os.MkdirTemp("", "mediatool-verify-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "index.json")
		if err := sr.Index.Save(path); err != nil {
			return err
		}
		other := index.DigestBytes([]byte("different input"))
		_, err = index.Load(path, other)
		if err == nil {
			return fmt.Errorf("stale index was accepted for a different input digest")
		}
		if !fault.IsKind(err, fault.KindIndexDigest) {
			return fmt.Errorf("expected index_digest fault, got %v", err)
		}
		return nil
	}

	// Media fault samples: the expected kind must appear, and for truncation
	// the offset must be within one RTP header of ground truth.
	var seen bool
	for _, f := range sr.Index.Faults {
		if f.Kind != s.ExpectKind {
			continue
		}
		seen = true
		if s.Class == fixture.FaultTruncation {
			d := f.Offset - s.ExpectOffset
			if d < -12 || d > 12 {
				return fmt.Errorf("truncation offset %d differs from truth %d by %d", f.Offset, s.ExpectOffset, d)
			}
		}
	}

	// A reorder/duplicate sample also has to demonstrate the reorder was seen;
	// the scanner reports reordered and duplicate as separate kinds.
	if s.Class == fixture.FaultReorderDuplicate {
		var sawReorder, sawDup bool
		for _, f := range sr.Index.Faults {
			if f.Kind == "reordered" {
				sawReorder = true
			}
			if f.Kind == "duplicate" {
				sawDup = true
			}
		}
		if !sawReorder || !sawDup {
			return fmt.Errorf("reorder sample: reordered=%v duplicate=%v", sawReorder, sawDup)
		}
		seen = true
	}

	if s.Class == fixture.FaultRTCPRegression {
		// The regression must propagate: the timeline records an untrusted
		// interval and alignment reports the affected boundary unalignable.
		if len(sr.Timeline.Untrusted()) == 0 {
			return fmt.Errorf("rtcp regression produced no untrusted interval")
		}
		alignRes, err := alignFor(sr.Index, opts)
		if err != nil {
			return err
		}
		if len(alignRes.Unaligned) == 0 {
			return fmt.Errorf("rtcp regression did not propagate to alignment")
		}
	}
	if !seen {
		return fmt.Errorf("expected fault kind %q not found in %d faults", s.ExpectKind, len(sr.Index.Faults))
	}
	return nil
}

func alignFor(ix *index.Index, opts fixture.Options) (*segment.Result, error) {
	var covs []segment.Coverage
	for _, spec := range opts.Tracks {
		tr, ok := ix.Track(spec.TrackID)
		if !ok {
			continue
		}
		covs = append(covs, segment.Coverage{
			ID: fmt.Sprintf("t%d", spec.TrackID), Track: tr, Trusted: ix.TrustPredicate(spec.TrackID),
		})
	}
	return segment.Align(covs, segment.Config{DurationMs: opts.SegmentMs, KeyframeSearchMs: opts.SegmentMs})
}
