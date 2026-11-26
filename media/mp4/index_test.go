package mp4

import (
	"testing"

	"replaykit/media/fault"
)

// buildSampleClip writes a 4-fragment, 2-sample-per-fragment stream.
func buildSampleClip(frameTicks uint32) *Builder {
	b := NewBuilder(90000, 1, frameTicks)
	b.WriteInit()
	for i := 0; i < 4; i++ {
		b.AddFragment(uint64(i)*uint64(frameTicks)*2, uint32(i+1), []SampleSpec{
			{Duration: frameTicks, Key: true, Payload: []byte("I-frame-payload")},
			{Duration: frameTicks, Payload: []byte("P-frame-payload")},
		})
	}
	return b
}

func buildIndex(t *testing.T, data []byte) (*Scan, *Index) {
	t.Helper()
	scan, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	idx, err := BuildIndex(data, scan, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	return scan, idx
}

func TestIndexParsesAllSamples(t *testing.T) {
	b := buildSampleClip(3000)
	_, idx := buildIndex(t, b.Bytes())
	if idx.Track.Timescale != 90000 {
		t.Fatalf("timescale = %d", idx.Track.Timescale)
	}
	if len(idx.Track.Samples) != 8 {
		t.Fatalf("samples = %d", len(idx.Track.Samples))
	}
	if len(idx.Keyframes) != 4 {
		t.Fatalf("keyframes = %v", idx.Keyframes)
	}
	if !idx.Complete {
		t.Fatalf("expected complete index: %+v", idx.Anomalies)
	}
	for i, s := range idx.Track.Samples {
		if s.Number != int64(i+1) {
			t.Fatalf("sample %d number = %d", i, s.Number)
		}
		if s.DecodeTime != int64(i)*int64(b.FrameTicks) {
			t.Fatalf("sample %d decode = %d", i, s.DecodeTime)
		}
		if s.Duration != int64(b.FrameTicks) {
			t.Fatalf("sample %d duration = %d", i, s.Duration)
		}
		if s.Key != (i%2 == 0) {
			t.Fatalf("sample %d key = %v", i, s.Key)
		}
	}
	if idx.CoveredEnd != 8*int64(b.FrameTicks) {
		t.Fatalf("covered end = %d", idx.CoveredEnd)
	}
	// Sample payloads must point at real bytes inside mdat.
	data := b.Bytes()
	for _, s := range idx.Track.Samples {
		if s.Offset < 0 || s.Offset+s.Size > int64(len(data)) {
			t.Fatalf("sample range out of input: %+v", s)
		}
	}
}

func TestIndexDigestIsDeterministic(t *testing.T) {
	b := buildSampleClip(3000)
	_, a := buildIndex(t, b.Bytes())
	_, c := buildIndex(t, b.Bytes())
	if a.Digest != c.Digest {
		t.Fatalf("digest unstable: %s vs %s", a.Digest, c.Digest)
	}
	if len(a.Digest) != 64 {
		t.Fatalf("digest length = %d", len(a.Digest))
	}
}

func TestIndexSidecarValid(t *testing.T) {
	b := buildSampleClip(3000)
	data := b.Bytes()
	scan, _ := ScanBytes(data, DefaultLimits())
	idx, err := BuildIndex(data, scan, DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	side := NewSidecar(idx.Track)
	if err := side.Verify(idx.Track); err != nil {
		t.Fatalf("verify: %v", err)
	}
	raw, err := MarshalSidecar(side)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSidecar(raw)
	if err != nil {
		t.Fatal(err)
	}
	idx2, err := BuildIndex(data, scan, DefaultLimits(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	if idx2.Rebuilt {
		t.Fatalf("valid sidecar must not force a rebuild: %+v", idx2.Anomalies)
	}
}

func TestIndexSidecarCorruptForcesRebuild(t *testing.T) {
	b := buildSampleClip(3000)
	data := b.Bytes()
	scan, _ := ScanBytes(data, DefaultLimits())
	idx, _ := BuildIndex(data, scan, DefaultLimits(), nil)
	side := NewSidecar(idx.Track)
	side.Samples[0].DecodeTime = 12345 // tamper without resealing
	if err := side.Verify(idx.Track); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	idx2, err := BuildIndex(data, scan, DefaultLimits(), side)
	if err != nil {
		t.Fatal(err)
	}
	if !idx2.Rebuilt {
		t.Fatal("corrupt sidecar must mark rebuilt")
	}
	found := false
	for _, a := range idx2.Anomalies {
		if a.Category == fault.CategoryIndexChecksum {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected checksum anomaly, got %+v", idx2.Anomalies)
	}
	// The rebuilt index still matches the freshly parsed truth.
	if idx2.Digest != idx.Digest {
		t.Fatalf("rebuilt digest %s != %s", idx2.Digest, idx.Digest)
	}
}

func TestIndexTruncatedLastFragmentRebuildsFromValidOnes(t *testing.T) {
	b := buildSampleClip(3000)
	b.TruncateLast(10) // cut into the final mdat
	data := b.Bytes()
	scan, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	idx, err := BuildIndex(data, scan, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if idx.Complete {
		t.Fatal("truncated input must not be complete")
	}
	if len(idx.Track.Samples) != 6 {
		t.Fatalf("samples = %d, want 6 from the three intact fragments", len(idx.Track.Samples))
	}
	if !idx.Rebuilt {
		t.Fatal("expected rebuilt flag")
	}
	sawOverflow := false
	for _, a := range idx.Anomalies {
		if a.Category == fault.CategorySampleOverflow || a.Category == fault.CategoryTruncatedBox {
			sawOverflow = true
			if a.Offset <= 0 {
				t.Fatalf("anomaly must carry a byte offset: %+v", a)
			}
		}
	}
	if !sawOverflow {
		t.Fatalf("expected an offset-bearing anomaly, got %+v", idx.Anomalies)
	}
}

func TestIndexQueries(t *testing.T) {
	b := buildSampleClip(3000)
	_, idx := buildIndex(t, b.Bytes())

	// Keys land at fragment starts: 0, 6000, 12000, 18000.
	s, ok := idx.KeyframeAtOrBefore(3000)
	if !ok || s.DecodeTime != 0 {
		t.Fatalf("KeyframeAtOrBefore = %+v %v", s, ok)
	}
	s, ok = idx.KeyframeAtOrBefore(6000)
	if !ok || s.DecodeTime != 6000 {
		t.Fatalf("KeyframeAtOrBefore exact = %+v %v", s, ok)
	}
	s, ok = idx.KeyframeAtOrAfter(3001)
	if !ok || s.DecodeTime != 6000 {
		t.Fatalf("KeyframeAtOrAfter = %+v %v", s, ok)
	}
	s, ok = idx.SampleContaining(4500)
	if !ok || s.DecodeTime != 3000 {
		t.Fatalf("SampleContaining = %+v %v", s, ok)
	}
	// [0,6001) touches samples 0..2 (0-3000, 3000-6000, 6000-9000).
	got := idx.SamplesInRange(0, 6001)
	if len(got) != 3 {
		t.Fatalf("SamplesInRange = %d", len(got))
	}
	if _, ok := idx.KeyframeAtOrAfter(1 << 40); ok {
		t.Fatal("expected no keyframe far past end")
	}
}

func TestIndexNoTimescaleIsFatal(t *testing.T) {
	// A moov without mdhd cannot map time at all.
	data := concat(wrap("ftyp", make([]byte, 8)), wrap("moov", wrap("trak", nil)))
	scan, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := BuildIndex(data, scan, DefaultLimits(), nil); !fault.IsFatal(err) {
		t.Fatalf("expected fatal, got %v", err)
	}
}

func TestIndexOutOfRangeSampleIsOffset(t *testing.T) {
	b := buildSampleClip(3000)
	data := b.Bytes()
	// Shrink the buffer so a sample range extends past the input without
	// changing the declared fragment lengths.
	shrunk := data[:len(data)-40]
	scan, err := ScanBytes(shrunk, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	idx, err := BuildIndex(shrunk, scan, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if idx.Complete {
		t.Fatal("expected incomplete index")
	}
}
