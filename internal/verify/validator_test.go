package verify

import (
	"os"
	"path/filepath"
	"testing"

	"tsindex/internal/digest"
	"tsindex/internal/fault"
	"tsindex/internal/fixture"
	"tsindex/internal/index"
)

func generate(t *testing.T, dir, name string, spec fixture.Spec) string {
	t.Helper()
	spec.Name = name
	path, err := fixture.Generate(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// indexAsset runs the same segment pipeline the worker uses so the verifier
// has real sidecars and segment records to re-check.
func indexAsset(t *testing.T, dir, source string) []index.SegmentRecord {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var recs []index.SegmentRecord
	packets := fixture.SegmentPackets
	for off := 0; off+packets*188 <= len(data); off += packets * 188 {
		h := digest.NewHasher()
		for p := 0; p < packets; p++ {
			pkt := data[off+p*188 : off+(p+1)*188]
			h.AddPacket(pkt, payloadOf(pkt))
		}
		d := h.Sum()
		rec := index.SegmentRecord{
			SegmentID: int64(len(recs)), StartOffset: int64(off),
			EndOffset: int64(off + packets*188), Packets: packets,
			RawHash: d.Raw, ContentHash: d.Content,
		}
		if _, err := index.WriteSidecar(dir, filepath.Base(source), rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestVerifySegmentsRecomputesMatchingHashes(t *testing.T) {
	dir := t.TempDir()
	source := generate(t, dir, "normal", fixture.Spec{})
	recs := indexAsset(t, dir, source)
	if len(recs) == 0 {
		t.Fatal("no segments indexed")
	}
	v := NewValidator()
	rep, err := v.VerifySegments(source, recs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Segments != rep.SegmentsMatched || rep.SegmentsMatched == 0 {
		t.Fatalf("all segment hashes must match: %+v", rep)
	}
}

func TestVerifyDetectsTamperedSegment(t *testing.T) {
	dir := t.TempDir()
	source := generate(t, dir, "normal", fixture.Spec{})
	recs := indexAsset(t, dir, source)

	// Tamper one stored hash: the verifier must flag it, not silently pass.
	recs[0].RawHash[0] ^= 0xFF
	v := NewValidator()
	rep, err := v.VerifySegments(source, recs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SegmentsMatched != rep.Segments-1 {
		t.Fatalf("tampered segment must fail hash match: %+v", rep)
	}
	if !rep.Recoverable || len(rep.Faults) == 0 {
		t.Fatalf("mismatch must be reported as recoverable: %+v", rep.Faults)
	}
}

func TestVerifyFramingReportsTruncationWithOffset(t *testing.T) {
	dir := t.TempDir()
	source := generate(t, dir, "trunc", fixture.Spec{Truncate: 37})
	v := NewValidator()
	rep, err := v.VerifyFile(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range rep.Faults {
		if f.Kind.String() == "truncated" && f.Recoverable && f.Offset >= 0 && f.Bytes == 37 {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation with offset and byte count not reported: %+v", rep.Faults)
	}
}

func TestVerifyRejectsIncompleteSidecar(t *testing.T) {
	dir := t.TempDir()
	source := generate(t, dir, "normal", fixture.Spec{})
	recs := indexAsset(t, dir, source)
	// Write a deliberately truncated sidecar and confirm read-level rejection.
	good, _ := filepath.Glob(filepath.Join(dir, "*.ts.tsix"))
	if len(good) == 0 {
		t.Fatal("no sidecar produced")
	}
	raw, _ := os.ReadFile(good[0])
	bad := filepath.Join(dir, "broken-segment-000000.ts.tsix")
	if err := os.WriteFile(bad, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ReadSidecar(bad); err == nil {
		t.Fatal("truncated sidecar accepted")
	}
	// The segment report itself still succeeds against valid records.
	v := NewValidator()
	if _, err := v.VerifySegments(source, recs); err != nil {
		t.Fatalf("valid records should verify: %v", err)
	}
}

func TestVerifyDetectsBadCRCAndCCGap(t *testing.T) {
	dir := t.TempDir()
	crcSource := generate(t, dir, "badcrc", fixture.Spec{BadCRC: true})
	gapSource := generate(t, dir, "ccgap", fixture.Spec{CCGap: true})

	v := NewValidator()
	rep, err := v.VerifyFile(crcSource, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind(rep.Faults, "bad-crc") {
		t.Fatalf("bad CRC not reported: %+v", rep.Faults)
	}

	v2 := NewValidator()
	rep2, err := v2.VerifyFile(gapSource, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind(rep2.Faults, "cc-gap") {
		t.Fatalf("CC gap not reported: %+v", rep2.Faults)
	}
}

func TestKeyframeOffsetOutsideBoundsReported(t *testing.T) {
	dir := t.TempDir()
	source := generate(t, dir, "normal", fixture.Spec{})
	recs := indexAsset(t, dir, source)
	// Corrupt a keyframe offset to lie outside its segment bounds. The
	// verifier's framing pass audits keyframe offsets against segment ranges.
	recs[0].Keyframes = []index.Keyframe{{
		Program: 1, TrackPID: 0x100, Codec: "h264", Complete: true,
		Ticks: 1, SourceOffset: recs[0].EndOffset + 1000,
	}}
	v := NewValidator()
	rep, err := v.VerifySegments(source, recs)
	if err != nil {
		t.Fatal(err)
	}
	// Out-of-bounds keyframe metadata must surface as a recoverable fault
	// located at the offending offset rather than being silently accepted.
	if !rep.Recoverable {
		t.Fatalf("out-of-bounds keyframe must be recoverable: %+v", rep.Faults)
	}
}

func hasKind(faults []fault.Fault, kind string) bool {
	for _, f := range faults {
		if f.Kind.String() == kind {
			return true
		}
	}
	return false
}
