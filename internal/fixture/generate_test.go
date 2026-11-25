package fixture

import (
	"bytes"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/index"
	"mediad/internal/stream"
)

func scan(t *testing.T, data []byte, codec stream.Codec) ([]*stream.AccessUnit, *stream.Scanner) {
	t.Helper()
	sc, err := stream.NewScanner(bytes.NewReader(data), codec, stream.DefaultConfig())
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	var aus []*stream.AccessUnit
	for {
		au, err := sc.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if au == nil {
			break
		}
		aus = append(aus, au)
	}
	return aus, sc
}

func TestGenerateExactIsPerfect(t *testing.T) {
	fx, err := Generate(Spec{Codec: stream.H264, Frames: 12, IndexMode: IndexExact})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	aus, _ := scan(t, fx.Stream, stream.H264)
	if len(aus) != 12 {
		t.Fatalf("AU count = %d, want 12", len(aus))
	}
	raws, _ := index.Parse(bytes.NewReader(fx.IndexText))
	idx, err := index.Validate(stream.H264, raws, int64(len(fx.Stream)))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if idx.InvalidCount != 0 {
		t.Fatalf("exact index has invalid entries: %+v", idx.Entries)
	}
	rep := index.MatchTimeline(aus, idx, clock.NewTimeBase(40_000_000))
	if rep.HitRate != 1.0 || rep.Hits != 12 || rep.Misses != 0 {
		t.Fatalf("exact hit rate = %f hits=%d misses=%d", rep.HitRate, rep.Hits, rep.Misses)
	}
}

func TestGenerateAllCodecsFrame(t *testing.T) {
	for _, codec := range []stream.Codec{stream.H264, stream.HEVC, stream.AV1} {
		fx, err := Generate(Spec{Codec: codec, Frames: 5, IndexMode: IndexExact})
		if err != nil {
			t.Fatalf("%s generate: %v", codec, err)
		}
		aus, _ := scan(t, fx.Stream, codec)
		if len(aus) != 5 {
			t.Fatalf("%s AU count = %d", codec, len(aus))
		}
		for i, au := range aus {
			if !au.Keyframe {
				t.Fatalf("%s frame %d not keyframe", codec, i)
			}
		}
	}
}

func TestShiftedIndexHitRateBand(t *testing.T) {
	// Non-truncated shifted: 2 misses out of 12 valid entries.
	fx, _ := Generate(Spec{Codec: stream.H264, Frames: 12, IndexMode: IndexShifted})
	aus, _ := scan(t, fx.Stream, stream.H264)
	raws, _ := index.Parse(bytes.NewReader(fx.IndexText))
	idx, _ := index.Validate(stream.H264, raws, int64(len(fx.Stream)))
	rep := index.MatchTimeline(aus, idx, clock.NewTimeBase(40_000_000))
	if rep.Hits != 10 || rep.Misses != 2 || rep.HitRate < 0.8 || rep.HitRate > 1.0 {
		t.Fatalf("shifted hit rate outside band: %+v", rep)
	}
	if len(rep.Matches) != 12 || rep.Matches[2].Hit || rep.Matches[9].Hit {
		t.Fatalf("shifted entries must be misses, not rebound: %+v", rep.Matches)
	}
}

func TestTruncatedStreamHasCorruptSegmentAndValidRangeHits(t *testing.T) {
	fx, err := Generate(Spec{Codec: stream.HEVC, Frames: 12, IndexMode: IndexShifted, Truncate: 7})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	aus, sc := scan(t, fx.Stream, stream.HEVC)
	if len(aus) != 8 { // 7 clean frames + 1 partial corrupt AU
		t.Fatalf("truncated AU count = %d, want 8", len(aus))
	}
	if !aus[7].Corrupt {
		t.Fatalf("partial AU must be corrupt: %+v", aus[7])
	}
	if len(sc.Damages()) == 0 {
		t.Fatal("no damage recorded for truncation")
	}
	damagedOff := sc.Damages()[0].Offset
	if damagedOff != aus[7].Start {
		t.Fatalf("damage offset %d != corrupt AU start %d", damagedOff, aus[7].Start)
	}

	raws, _ := index.Parse(bytes.NewReader(fx.IndexText))
	idx, err := index.Validate(stream.HEVC, raws, int64(len(fx.Stream)))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Frames 0..7 fall inside the truncated range (frame 7 points at the
	// partial AU); frames 8..11 are out of range and invalid. Frame 2 is
	// shifted, leaving 7 hits / 8 valid = 0.875, inside the acceptance band.
	if idx.ValidCount != 8 {
		t.Fatalf("valid entries = %d, want 8", idx.ValidCount)
	}
	rep := index.MatchTimeline(aus, idx, clock.NewTimeBase(40_000_000))
	if rep.Hits != 7 || rep.Misses != 1 || rep.HitRate != 0.875 {
		t.Fatalf("truncated hit rate wrong: hits=%d misses=%d rate=%f",
			rep.Hits, rep.Misses, rep.HitRate)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, _ := Generate(Spec{Codec: stream.AV1, Frames: 9, IndexMode: IndexExact})
	b, _ := Generate(Spec{Codec: stream.AV1, Frames: 9, IndexMode: IndexExact})
	if !bytes.Equal(a.Stream, b.Stream) || !bytes.Equal(a.IndexText, b.IndexText) {
		t.Fatal("regenerated fixture differs byte-for-byte")
	}
	if HashBytes(a.Stream) != HashBytes(b.Stream) {
		t.Fatal("fixture hash not stable")
	}
}
