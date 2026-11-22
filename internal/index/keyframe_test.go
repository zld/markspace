package index

import (
	"strings"
	"testing"

	"mediadist/internal/codec"
)

// sample builds one Annex B H.264 sample with an optional IDR slice.
func sample(key bool, salt byte) []byte {
	var out []byte
	for _, nal := range [][]byte{
		{0x67, 0x42, 0x00, salt},
		{0x68, 0xCE, 0x06, salt},
	} {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	slice := []byte{0x41, salt, salt}
	if key {
		slice[0] = 0x65
	}
	out = append(out, 0, 0, 0, 1)
	out = append(out, slice...)
	return out
}

// scansFor builds scans with synthetic timestamps 1000, 2000, ... so tests
// exercise timestamp-keyed matching rather than ordinals.
func scansFor(samples [][]byte) []SampleScan {
	out := make([]SampleScan, 0, len(samples))
	for i, data := range samples {
		out = append(out, SampleScan{
			Stream: 0, Sample: i, Timestamp: uint32(1000 * (i + 1)),
			Length: int64(len(data)),
			Result: codec.ScanH264(0, data, codec.DefaultScanLimits()),
		})
	}
	return out
}

func ts(i int) uint32 { return uint32(1000 * (i + 1)) }

// keyOffset returns the offset of the IDR unit in a sample.
func keyOffset(t *testing.T, data []byte) int64 {
	t.Helper()
	res := codec.ScanH264(0, data, codec.DefaultScanLimits())
	if len(res.Keyframes) != 1 {
		t.Fatalf("sample has %d keyframes", len(res.Keyframes))
	}
	return res.Units[res.Keyframes[0]].Offset
}

func TestVerifyAcceptsMatchingIndex(t *testing.T) {
	samples := [][]byte{sample(true, 1), sample(false, 2), sample(true, 3)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: keyOffset(t, samples[0]), Size: 4, Keyframe: true},
		{Stream: 0, Sample: 2, Timestamp: ts(2), Offset: keyOffset(t, samples[2]), Size: 4, Keyframe: true},
		{Stream: 0, Sample: 1, Timestamp: ts(1), Offset: 0, Size: 4, Keyframe: false},
	}}
	v := Verify(idx, scansFor(samples))
	if len(v.Mismatches) != 0 {
		t.Fatalf("mismatches %+v", v.Mismatches)
	}
	if v.Matched != 3 || v.Total != 3 {
		t.Fatalf("matched %d/%d", v.Matched, v.Total)
	}
}

func TestVerifyDetectsOffsetOutOfRange(t *testing.T) {
	samples := [][]byte{sample(true, 1), sample(false, 2)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: 1 << 16, Size: 4, Keyframe: true},
		{Stream: 0, Sample: 1, Timestamp: ts(1), Offset: 0, Size: 4, Keyframe: false},
	}}
	v := Verify(idx, scansFor(samples))
	// The out-of-range row is a mismatch, and its omitted keyframe is also
	// reported as a recall failure. Both are expected and independent.
	var sawOffset, sawMissing bool
	for _, m := range v.Mismatches {
		switch m.Kind {
		case MismatchOffset:
			sawOffset = true
			if m.Span.Offset != 1<<16 || m.Span.Length != 4 {
				t.Fatalf("span %+v", m.Span)
			}
		case MismatchMissingKey:
			sawMissing = true
		}
	}
	if !sawOffset {
		t.Fatalf("offset mismatch missing from %+v", v.Mismatches)
	}
	if !sawMissing {
		t.Fatal("the keyframe the bad row referred to must be reported missing")
	}
}

func TestVerifyDetectsFalseKeyframeClaim(t *testing.T) {
	samples := [][]byte{sample(true, 1), sample(false, 2)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: keyOffset(t, samples[0]), Size: 4, Keyframe: true},
		// Parameter set at offset 0 is not a keyframe unit.
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: 0, Size: 4, Keyframe: true},
		{Stream: 0, Sample: 1, Timestamp: ts(1), Offset: 0, Size: 4, Keyframe: false},
	}}
	v := Verify(idx, scansFor(samples))
	if len(v.Mismatches) != 1 || v.Mismatches[0].Kind != MismatchNotKey {
		t.Fatalf("mismatches %+v", v.Mismatches)
	}
}

func TestVerifyDetectsMissedKeyframe(t *testing.T) {
	samples := [][]byte{sample(true, 1), sample(true, 2)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Offset: keyOffset(t, samples[0]), Size: 4, Keyframe: true},
	}}
	v := Verify(idx, scansFor(samples))
	found := false
	for _, m := range v.Mismatches {
		if m.Kind == MismatchMissingKey && m.Entry.Timestamp == ts(1) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missed keyframe not reported: %+v", v.Mismatches)
	}
}

func TestCheckAndRebuildReplacesBadIndex(t *testing.T) {
	samples := [][]byte{sample(true, 1), sample(false, 2), sample(true, 3)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: 1 << 16, Size: 4, Keyframe: true},
	}}
	v, effective := CheckAndRebuild(idx, scansFor(samples))
	if len(v.Mismatches) == 0 {
		t.Fatal("expected mismatches")
	}
	if effective == idx {
		t.Fatal("bad index must be replaced")
	}
	if effective == nil || len(effective.Entries) != 2 {
		t.Fatalf("rebuilt entries %+v", effective)
	}
	for _, e := range effective.Entries {
		if !e.Keyframe || !e.Sync {
			t.Fatalf("rebuilt row %+v", e)
		}
	}
	// Rebuilding the rebuilt index must be a no-op (idempotent).
	v2, effective2 := CheckAndRebuild(effective, scansFor(samples))
	if len(v2.Mismatches) != 0 || effective2 != effective {
		t.Fatalf("rebuilt index is not stable: %+v", v2.Mismatches)
	}
}

func TestCheckAndRebuildKeepsGoodIndex(t *testing.T) {
	samples := [][]byte{sample(true, 1)}
	idx := &Index{Version: 1, Entries: []Entry{
		{Stream: 0, Sample: 0, Timestamp: ts(0), Offset: keyOffset(t, samples[0]), Size: 4, Keyframe: true},
	}}
	_, effective := CheckAndRebuild(idx, scansFor(samples))
	if effective != idx {
		t.Fatal("a verified index must not be rebuilt")
	}
}

func TestLoadRejectsMalformedAndOversize(t *testing.T) {
	if _, err := Load(strings.NewReader(`{"version":1,"entries":[]}`), Limits{}); err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
	if _, err := Load(strings.NewReader(`{"version":1,"entries":[]} trailing`), Limits{}); err == nil {
		t.Fatal("trailing data must be rejected")
	}
	if _, err := Load(strings.NewReader(`{"version":1,"entries":[],"extra":1}`), Limits{}); err == nil {
		t.Fatal("unknown field must be rejected")
	}
	big := `{"version":1,"entries":[{"stream":0,"sample":0,"timestamp":1,"offset":0,"size":4,"keyframe":true}]}`
	if _, err := Load(strings.NewReader(big), Limits{MaxBytes: 8}); err == nil {
		t.Fatal("oversize body must be rejected")
	}
	if _, err := Load(strings.NewReader(big), Limits{MaxEntries: 0, MaxBytes: 1 << 20, MaxUnitSize: 2}); err == nil {
		t.Fatal("oversize unit must be rejected")
	}
}

func TestRebuildIsSortedAndDeterministic(t *testing.T) {
	samples := [][]byte{sample(true, 3), sample(true, 1), sample(false, 2)}
	scans := scansFor(samples)
	a := Rebuild(scans)
	b := Rebuild(scans)
	if len(a.Entries) != len(b.Entries) {
		t.Fatalf("entry counts differ")
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			t.Fatalf("rebuild is not deterministic: %+v vs %+v", a.Entries, b.Entries)
		}
	}
	for i := 1; i < len(a.Entries); i++ {
		if a.Entries[i-1].Timestamp > a.Entries[i].Timestamp {
			t.Fatalf("entries not sorted: %+v", a.Entries)
		}
	}
}

func TestHitRate(t *testing.T) {
	v := Verification{Total: 10, Matched: 9}
	if v.HitRate() != 0.9 {
		t.Fatalf("hit rate %v", v.HitRate())
	}
	empty := Verification{}
	if empty.HitRate() != 1 {
		t.Fatalf("empty hit rate %v", empty.HitRate())
	}
}
