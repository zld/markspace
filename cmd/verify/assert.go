package main

import (
	"fmt"
	"reflect"

	"example.com/media-kernel/damage"
	"example.com/media-kernel/pipeline"
	"example.com/media-kernel/sample"
)

func assertReport(f *sample.Fixture, r *pipeline.Result, rec *pipeline.RecoveryReport, limit int64) error {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}

	if r.NormalMP4.RoundTrip.Checked == 0 || r.NormalMP4.RoundTrip.Rate != 1 {
		fail("round-trip rate=%.3f checked=%d, want 1.0",
			r.NormalMP4.RoundTrip.Rate, r.NormalMP4.RoundTrip.Checked)
	}
	if r.NormalMP4.Index.HitRate() != 1 || r.NormalMP4.Index.HitCount != r.NormalMP4.Index.IndexedCount {
		fail("keyframe hit rate=%.3f hits=%d/%d, want 1.0",
			r.NormalMP4.Index.HitRate(), r.NormalMP4.Index.HitCount, r.NormalMP4.Index.IndexedCount)
	}
	if len(r.Segments) != 4 {
		fail("segments=%d want 4", len(r.Segments))
	}
	seenHashes := map[string]bool{}
	for _, seg := range r.Segments {
		if len(seg.SHA256) != 64 {
			fail("segment %d has invalid SHA-256 length %d", seg.Index, len(seg.SHA256))
		}
		if seenHashes[seg.SHA256] {
			fail("segment %d duplicates another segment hash", seg.Index)
		}
		seenHashes[seg.SHA256] = true
		if seg.ByteRange.End <= seg.ByteRange.Start {
			fail("segment %d has empty byte range", seg.Index)
		}
	}

	if r.DuplicatePackets != f.ExpectedDuplicates {
		fail("duplicates=%d want %d", r.DuplicatePackets, f.ExpectedDuplicates)
	}
	if !reflect.DeepEqual(r.MissingSequences, f.ExpectedMissing) {
		fail("missing=%v want %v", r.MissingSequences, f.ExpectedMissing)
	}

	var hasTruncated, hasGap, hasCorruptKeyframe bool
	var corruptIntervalNonEmpty bool
	var gapTimeNonEmpty bool
	for _, region := range r.Damage.Regions {
		switch region.Kind {
		case damage.KindMalformedPacket:
			if region.TimeRange != nil {
				hasTruncated = true
			}
		case damage.KindMissingPacket:
			hasGap = true
			if region.TimeRange != nil && region.TimeRange.End.Ticks > region.TimeRange.Start.Ticks {
				gapTimeNonEmpty = true
			}
		case damage.KindCorruptKeyframe:
			hasCorruptKeyframe = true
			if region.ByteRange != nil && region.ByteRange.End > region.ByteRange.Start {
				corruptIntervalNonEmpty = true
			}
		}
	}
	if !hasTruncated {
		fail("expected a locatable truncated-packet region")
	}
	if !hasGap || !gapTimeNonEmpty {
		fail("expected a missing-packet region with non-empty confirmable time interval")
	}
	if !hasCorruptKeyframe || !corruptIntervalNonEmpty {
		fail("expected a corrupt-keyframe region with non-empty byte interval")
	}

	if rec == nil || len(rec.BeforeHashes) != 2 || len(rec.AfterHashes) < 2 {
		fail("recovery report incomplete: %+v", rec)
	} else {
		for i, before := range rec.BeforeHashes {
			if rec.AfterHashes[i] != before {
				fail("segment %d hash changed across recovery", i)
			}
		}
		if rec.ResumedFrom != 2 {
			fail("resumed_from=%d want 2", rec.ResumedFrom)
		}
	}

	if r.PeakTrackedBytes <= 0 {
		fail("peak tracked memory was not recorded")
	}
	if r.PeakTrackedBytes > limit {
		fail("peak tracked memory %d exceeds limit %d", r.PeakTrackedBytes, limit)
	}

	if len(failures) > 0 {
		msg := "acceptance assertions failed:"
		for _, item := range failures {
			msg += "\n  - " + item
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
