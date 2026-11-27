package check

import (
	"testing"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/merge"
	"mediapipeline/internal/transport"
)

// h264Stream builds four length-prefixed NAL units; indices 0 and 2 are IDR.
func h264Stream() []byte {
	var s []byte
	s = append(s, h264NAL(5, []byte{0xAA, 0xBB})...)
	s = append(s, h264NAL(1, []byte{0x01})...)
	s = append(s, h264NAL(5, []byte{0xCC, 0xDD})...)
	s = append(s, h264NAL(1, []byte{0x02})...)
	return s
}

func h264NAL(typ byte, p []byte) []byte {
	payload := append([]byte{typ & 0x1F}, p...)
	out := make([]byte, 4+len(payload))
	out[0] = byte(len(payload) >> 24)
	out[1] = byte(len(payload) >> 16)
	out[2] = byte(len(payload) >> 8)
	out[3] = byte(len(payload))
	copy(out[4:], payload)
	return out
}

func goodSetup(t *testing.T) (Input, []ingest.Unit) {
	t.Helper()
	stream := h264Stream()
	var rep fault.Report
	units, err := ingest.Scan(ingest.CodecH264, stream, ingest.DefaultLimits, &rep)
	if err != nil || rep.Count() != 0 {
		t.Fatalf("setup scan: %v %v", err, rep.All())
	}
	idx := &ingest.IndexFile{
		Codec: ingest.CodecH264,
		Base:  clock.MustRational(1, 90000),
		Entries: []ingest.IndexEntry{
			{UnitIndex: 0, Tick: 0, Offset: units[0].Offset, Size: uint32(len(units[0].Payload)), Seconds: clock.MustRational(0, 1)},
			{UnitIndex: 2, Tick: 90, Offset: units[2].Offset, Size: uint32(len(units[2].Payload)), Seconds: clock.MustRational(1, 1000)},
		},
	}
	entry := func(key string, u int) merge.Entry {
		return merge.Entry{
			Asset: merge.Asset{
				Key: key, UnitIndex: u, Offset: units[u].Offset,
				Size:     int64(len(units[u].Payload)),
				TimeFrom: clock.MustRational(int64(u), 1),
				TimeTo:   clock.MustRational(int64(u+1), 1),
			},
			Payload: units[u].Payload,
		}
	}
	m, err := merge.Build([]merge.Version{{Name: "v1", Entries: []merge.Entry{
		entry("a", 0), entry("b", 2),
	}}}, &fault.Report{})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := merge.CanonicalBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Codec: ingest.CodecH264, Stream: stream, Index: idx, Manifest: m,
		GapSSRC: 1, Gaps: []transport.Gap{{SSRC: 1, From: 4, To: 6}},
		Staged: staged, Limits: ingest.DefaultLimits,
	}, units
}

func TestValidatePassesCleanInput(t *testing.T) {
	in, _ := goodSetup(t)
	res := Validate(in)
	if !res.Pass {
		t.Fatalf("clean input must pass, got %+v", res.Faults)
	}
	if len(res.Digest) != 64 {
		t.Fatalf("digest %q is not a sha256 hex", res.Digest)
	}
}

func TestValidateRecomputesDigestIndependently(t *testing.T) {
	in, _ := goodSetup(t)
	// Tamper the staged bytes while leaving the manifest object untouched:
	// only an independent re-hash of the staged bytes can catch this.
	in.Staged = append([]byte(nil), in.Staged...)
	in.Staged[len(in.Staged)-1] ^= 0xFF
	res := Validate(in)
	if res.Pass || !hasCode(res.Faults, fault.CodeChecksumMismatch) {
		t.Fatalf("tampered staged bytes must fail checksum, got %+v", res.Faults)
	}
}

func TestValidateDanglingIndexAndAsset(t *testing.T) {
	in, units := goodSetup(t)
	// Index points at a unit that does not exist after a truncating rescan.
	in.Index.Entries = append(in.Index.Entries, ingest.IndexEntry{
		UnitIndex: 99, Tick: 180, Offset: 1234, Size: 1, Seconds: clock.MustRational(2, 1000),
	})
	// Asset references a unit beyond the rescanned set.
	in.Manifest.Assets = append(in.Manifest.Assets, merge.Asset{
		Key: "ghost", UnitIndex: 40, Offset: 9999, Size: 2,
		TimeFrom: clock.MustRational(3, 1), TimeTo: clock.MustRational(4, 1), Digest: "ab",
	})
	// The stream itself is truncated: drop the final unit so rescan differs.
	in.Stream = in.Stream[:len(h264Stream())-len(units[3].Payload)-4]
	res := Validate(in)
	if res.Pass {
		t.Fatal("damaged input must not pass")
	}
	if !hasCode(res.Faults, fault.CodeIndexOutOfBounds) {
		t.Fatalf("expected dangling index fault, got %+v", res.Faults)
	}
}

func TestValidateNonMonotonicTimestamps(t *testing.T) {
	in, _ := goodSetup(t)
	in.Manifest.Assets[1].TimeFrom = clock.MustRational(0, 1) // before asset a end
	res := Validate(in)
	if res.Pass || !hasCode(res.Faults, fault.CodeNonMonotonic) {
		t.Fatalf("expected non-monotonic fault, got %+v", res.Faults)
	}
}

func TestValidateOverlappingGaps(t *testing.T) {
	in, _ := goodSetup(t)
	in.Gaps = []transport.Gap{
		{SSRC: 1, From: 2, To: 5},
		{SSRC: 1, From: 4, To: 7}, // overlaps
	}
	res := Validate(in)
	if res.Pass || !hasCode(res.Faults, fault.CodeBoundaryInvalid) {
		t.Fatalf("expected gap boundary fault, got %+v", res.Faults)
	}
}

func TestValidateWrongAssetDigest(t *testing.T) {
	in, _ := goodSetup(t)
	in.Manifest.Assets[0].Digest = "00" // claim a digest the bytes do not have
	res := Validate(in)
	if res.Pass || !hasCode(res.Faults, fault.CodeChecksumMismatch) {
		t.Fatalf("expected asset checksum mismatch, got %+v", res.Faults)
	}
}

func hasCode(faults []fault.Fault, code fault.Code) bool {
	for _, f := range faults {
		if f.Code == code {
			return true
		}
	}
	return false
}
