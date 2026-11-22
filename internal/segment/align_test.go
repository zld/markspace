package segment

import (
	"testing"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/timeline"
)

// synthStream builds an Annex B representation whose keyframes land on
// the given PTS list (one keyframe plus inter units at 40 ms spacing).
func synthStream(codec bitstream.Codec, keyPTS []timeline.Ticks, skew timeline.Ticks) ([]byte, []index.Entry) {
	var data []byte
	var entries []index.Entry
	seq := uint32(0)
	frame := timeline.MustMs(40)
	for k, pts := range keyPTS {
		kpts := pts + skew
		body := []byte{byte('k'), byte(k)}
		data = append(data, annexB(codec, seq, kpts, true, body)...)
		entries = append(entries, index.Entry{Seq: seq, Offset: int64(len(data) - unitLen(codec, body)), PTS: kpts})
		seq++
		// two inter frames between keyframes
		for j := 1; j <= 2; j++ {
			data = append(data, annexB(codec, seq, kpts+timeline.Ticks(j)*frame, false, []byte("p"))...)
			seq++
		}
	}
	return data, entries
}

func annexB(codec bitstream.Codec, seq uint32, pts timeline.Ticks, key bool, body []byte) []byte {
	typ := uint32(1)
	if codec == bitstream.CodecH265 {
		typ = 1
	}
	hdr := []byte{byte(typ)}
	if codec == bitstream.CodecH265 {
		hdr = []byte{byte(typ << 1), 1}
	}
	if key {
		if codec == bitstream.CodecH264 {
			hdr[0] = 5
		} else {
			hdr[0] = 19 << 1
		}
	}
	meta := bitstream.EncodeMeta(seq, pts, uint32(len(body)), bitstream.CRC32(body))
	inner := append(append([]byte{}, meta[:]...), body...)
	out := append([]byte{0, 0, 0, 1}, hdr...)
	return append(out, bitstream.EscapeEPB(inner)...)
}

func unitLen(codec bitstream.Codec, body []byte) int {
	return len(annexB(codec, 0, 0, true, body))
}

func planFor(t *testing.T, repID string, codec bitstream.Codec, data []byte, entries []index.Entry) *Plan {
	t.Helper()
	scan, err := bitstream.Scan(codec, data, bitstream.DefaultLimits())
	if err != nil {
		t.Fatalf("scan %s: %v", repID, err)
	}
	rec := index.Reconcile(&index.File{Stream: repID, Entries: entries}, scan)
	plan, err := Build(repID, scan, rec, Config{TargetDuration: timeline.MustMs(120)})
	if err != nil {
		t.Fatalf("plan %s: %v", repID, err)
	}
	return plan
}

func TestAlignSharedBoundariesWithinTolerance(t *testing.T) {
	keys := []timeline.Ticks{0, timeline.MustMs(120), timeline.MustMs(240), timeline.MustMs(360)}
	d264, e264 := synthStream(bitstream.CodecH264, keys, 0)
	d265, e265 := synthStream(bitstream.CodecH265, keys, timeline.MustMs(15))
	p264 := planFor(t, "h264-720p", bitstream.CodecH264, d264, e264)
	p265 := planFor(t, "h265-1080p", bitstream.CodecH265, d265, e265)

	a, err := Align([]*Plan{p264, p265}, DefaultAlignConfig())
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if a.Boundaries != 4 || len(a.Targets) != 4 {
		t.Fatalf("boundaries = %d, want 4", a.Boundaries)
	}
	if a.SkewMaxMs < 14.9 || a.SkewMaxMs > 15.1 {
		t.Fatalf("skew = %.2fms, want ~15ms", a.SkewMaxMs)
	}
	for b, seg := range p264.Segments {
		if seg.Index != b || seg.IndexSource != SourceAligned {
			t.Fatalf("segment %d not stamped aligned: %+v", b, seg)
		}
	}
}

func TestAlignRejectsSkewBeyondTolerance(t *testing.T) {
	keys := []timeline.Ticks{0, timeline.MustMs(120), timeline.MustMs(240)}
	d264, e264 := synthStream(bitstream.CodecH264, keys, 0)
	dAv1, eAv1 := synthStream(bitstream.CodecH265, keys, timeline.MustMs(80))
	p1 := planFor(t, "a", bitstream.CodecH264, d264, e264)
	p2 := planFor(t, "b", bitstream.CodecH265, dAv1, eAv1)
	_, err := Align([]*Plan{p1, p2}, AlignConfig{Tolerance: timeline.MustMs(40)})
	if err == nil {
		t.Fatal("want skew rejection")
	}
	te, ok := err.(*timeline.Error)
	if !ok || te.Kind != timeline.KindSkewExceeded || te.Action != "reject_group" {
		t.Fatalf("want skew_exceeded/reject_group, got %v", err)
	}
}

func TestAlignRejectsBoundaryCountMismatch(t *testing.T) {
	keysA := []timeline.Ticks{0, timeline.MustMs(120), timeline.MustMs(240)}
	keysB := []timeline.Ticks{0, timeline.MustMs(120), timeline.MustMs(240), timeline.MustMs(360)}
	dA, eA := synthStream(bitstream.CodecH264, keysA, 0)
	dB, eB := synthStream(bitstream.CodecH265, keysB, 0)
	pA := planFor(t, "a", bitstream.CodecH264, dA, eA)
	pB := planFor(t, "b", bitstream.CodecH265, dB, eB)
	if _, err := Align([]*Plan{pA, pB}, DefaultAlignConfig()); err == nil {
		t.Fatal("want boundary count mismatch error")
	}
}

func TestPlanNoOverlapAndMonotonicTime(t *testing.T) {
	keys := []timeline.Ticks{0, timeline.MustMs(120), timeline.MustMs(240), timeline.MustMs(360)}
	data, entries := synthStream(bitstream.CodecH264, keys, 0)
	p := planFor(t, "solo", bitstream.CodecH264, data, entries)
	for i, s := range p.Segments {
		if s.EndOffset <= s.StartOffset || s.EndPTS <= s.StartPTS {
			t.Fatalf("segment %d has non-positive span", i)
		}
		if i > 0 {
			if s.StartOffset != p.Segments[i-1].EndOffset {
				t.Fatalf("segment %d offset gap/overlap: %d != %d", i, s.StartOffset, p.Segments[i-1].EndOffset)
			}
			if s.StartPTS <= p.Segments[i-1].StartPTS {
				t.Fatalf("segment %d timestamps not monotonic", i)
			}
		}
	}
}
