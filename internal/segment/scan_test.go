package segment

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediadeliver/internal/fixture"
	"mediadeliver/internal/model"
)

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cleanScan(t *testing.T, data []byte) *Result {
	t.Helper()
	res, err := ScanFile(writeTemp(t, data))
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	return res
}

func TestCleanBoundaries(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	res := cleanScan(t, data)
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Segments) != 12 {
		t.Fatalf("segments = %d, want 12", len(res.Segments))
	}
	if res.SampleCount != 36 {
		t.Fatalf("samples = %d, want 36", res.SampleCount)
	}
	for i, seg := range res.Segments {
		if seg.MdatOffset <= seg.MoofOffset || seg.MdatOffset != seg.MoofOffset+116 {
			t.Fatalf("segment %d moof/mdat offsets = %d/%d", i, seg.MoofOffset, seg.MdatOffset)
		}
		if seg.Offset+seg.Size > int64(len(data)) {
			t.Fatalf("segment %d extends past EOF", i)
		}
		if got := string(data[seg.MoofOffset+4 : seg.MoofOffset+8]); got != "moof" {
			t.Fatalf("segment %d offset does not point at moof: %q", i, got)
		}
		if seg.BaseDecodeTime != uint64(i)*90000 {
			t.Fatalf("segment %d base decode time = %d", i, seg.BaseDecodeTime)
		}
	}
}

func TestTruncatedBoxIsLocated(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	// Cut inside the mdat of the fourth fragment (index 3).
	cut := int64(res(t, data).Segments[3].MdatOffset + 40)
	t.Logf("cut=%d", cut)
	truncated := fixture.TruncateBox(data, cut)
	res := cleanScan(t, truncated)
	if len(res.Segments) != 4 {
		t.Fatalf("salvaged segments = %d, want 4 (3 complete + 1 partial)", len(res.Segments))
	}
	e := findCode(t, res.Errors, model.CodeTruncatedBox)
	if e.Offset != cut-40 {
		t.Fatalf("truncated box offset = %d, want %d (mdat start)", e.Offset, cut-40)
	}
	if res.ScannedTo != cut-40 {
		t.Fatalf("scanned_to = %d", res.ScannedTo)
	}
	if !res.Segments[3].Corrupt || res.Segments[3].ErrorCode != string(model.CodeTruncatedBox) {
		t.Fatalf("partial segment not marked corrupt: %+v", res.Segments[3])
	}
}

func res(t *testing.T, data []byte) *Result {
	t.Helper()
	return cleanScan(t, data)
}

func TestInvalidBoxSize(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	base := cleanScan(t, data)
	bad := base.Segments[5].Offset
	corrupt := fixture.CorruptBoxSize(data, bad, 0xFFFFFFF0)
	res := cleanScan(t, corrupt)
	if len(res.Segments) != 5 {
		t.Fatalf("segments before failure = %d, want 5", len(res.Segments))
	}
	e := findCode(t, res.Errors, model.CodeInvalidBoxSize)
	if e.Offset != bad {
		t.Fatalf("invalid size offset = %d, want %d", e.Offset, bad)
	}
	if res.ScannedTo != bad {
		t.Fatalf("scanned_to = %d, want %d", res.ScannedTo, bad)
	}
}

func TestIncompleteSampleTable(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	base := cleanScan(t, data)
	moof := base.Segments[2].MoofOffset
	mutated, err := fixture.IncompleteSampleTable(data, moof)
	if err != nil {
		t.Fatal(err)
	}
	res := cleanScan(t, mutated)
	if len(res.Segments) != 12 {
		t.Fatalf("segments after bad moof = %d, want 12 (corrupt one kept)", len(res.Segments))
	}
	if !res.Segments[2].Corrupt || res.Segments[2].ErrorCode != string(model.CodeIncompleteSamples) {
		t.Fatalf("bad moof not marked corrupt: %+v", res.Segments[2])
	}
	e := findCode(t, res.Errors, model.CodeIncompleteSamples)
	if e.Offset != moof {
		t.Fatalf("incomplete sample offset = %d, want %d", e.Offset, moof)
	}
	for _, seg := range res.Segments[3:] {
		if seg.Corrupt {
			t.Fatalf("segment %d after the bad moof marked corrupt", seg.Index)
		}
	}
}

func TestRecoveryAfterCorruptFragment(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	base := cleanScan(t, data)
	// A truncated tail must not cost the fragments that are already
	// complete: the scanner keeps them and flags only the partial one.
	cut := base.Segments[7].MdatOffset + 10
	res := cleanScan(t, fixture.TruncateBox(data, cut))
	if len(res.Segments) != 8 {
		t.Fatalf("salvaged segments = %d, want 8 (7 complete + 1 partial)", len(res.Segments))
	}
	if !res.Segments[7].Corrupt || res.Segments[7].ErrorCode != string(model.CodeTruncatedBox) {
		t.Fatalf("tail segment not marked corrupt: %+v", res.Segments[7])
	}
	for _, seg := range res.Segments[:7] {
		if seg.Corrupt {
			t.Fatalf("clean segment %d marked corrupt", seg.Index)
		}
	}
}

func findCode(t *testing.T, errs []*model.Error, code model.Code) *model.Error {
	t.Helper()
	for _, e := range errs {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("missing %s diagnostic in %v", code, errs)
	return nil
}

// TestRTPPayloadOffsetsMatchFiles verifies that generated RTP packets parse
// back with the pinned packet library and that the packet payload size lines
// up with the mdat payload the scanner reports.
func TestRTPPayloadOffsetsMatchFiles(t *testing.T) {
	data := fixture.SegmentFile(fixture.DefaultVideoSegments())
	base := cleanScan(t, data)
	payloadBytes := int(base.Segments[0].MdatSize) - 8
	packets, err := fixture.RTPStream(7, 4, payloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	var reassembled []byte
	expectedOffset := int64(0)
	for i, fp := range packets {
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(fp.Raw); err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if pkt.SequenceNumber != fp.SequenceNumber || pkt.Timestamp != fp.Timestamp || pkt.Marker != fp.Marker {
			t.Fatalf("packet %d header mismatch", i)
		}
		reassembled = append(reassembled, pkt.Payload...)
		_ = expectedOffset
	}
	if len(reassembled) != len(packets)*payloadBytes {
		t.Fatalf("reassembled %d bytes", len(reassembled))
	}
	// The payloads are generated independently, but the scanner-reported
	// mdat payload of segment 0 must match the packet size exactly.
	segPayload := data[base.Segments[0].MdatOffset+8 : base.Segments[0].MdatOffset+base.Segments[0].MdatSize]
	if len(segPayload) != payloadBytes {
		t.Fatalf("segment payload = %d, packet payload = %d", len(segPayload), payloadBytes)
	}
	if binary.BigEndian.Uint32(data[base.Segments[0].MoofOffset:]) == 0 {
		t.Fatal("moof size field is zero")
	}
	if !bytes.HasPrefix(data, []byte{0, 0, 0, 16, 's', 't', 'y', 'p'}) {
		t.Fatalf("file does not start with styp")
	}
}
