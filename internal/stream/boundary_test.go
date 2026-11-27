package stream

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	perr "gamepack/internal/errors"
)

func rtpFile(t *testing.T, dir string, packets []*rtp.Packet) string {
	t.Helper()
	var out []byte
	for _, p := range packets {
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		hdr := make([]byte, 2)
		binary.LittleEndian.PutUint16(hdr, uint16(len(b)))
		out = append(out, hdr...)
		out = append(out, b...)
	}
	path := filepath.Join(dir, "src.rtp")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkPacket(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: 96},
		Payload: []byte{0xaa},
	}
}

func TestReadRTPAndAttach(t *testing.T) {
	sp, ip := writeFiles(t, t.TempDir(), "v", h264Stream(), h264IndexGood())
	res, err := ScanFile(CodecH264, sp, ip)
	if err != nil {
		t.Fatal(err)
	}
	pkts := []*rtp.Packet{
		mkPacket(10, 9000), mkPacket(11, 12000), mkPacket(12, 15000), mkPacket(13, 18000),
	}
	path := rtpFile(t, t.TempDir(), pkts)
	got, err := ReadRTPSource(path)
	if err != nil || len(got) != 4 {
		t.Fatalf("ReadRTPSource: %v len=%d", err, len(got))
	}
	if err := AttachTransportCoordinates(res, got); err != nil {
		t.Fatalf("attach: %v", err)
	}
	b := res.Boundaries[0] // ordinal 2 -> third packet
	if b.RTPTimestamp != 15000 || b.RTPSeq != 12 {
		t.Fatalf("boundary transport = ts %d seq %d", b.RTPTimestamp, b.RTPSeq)
	}
}

func TestAttachCountMismatch(t *testing.T) {
	sp, ip := writeFiles(t, t.TempDir(), "v", h264Stream(), h264IndexGood())
	res, err := ScanFile(CodecH264, sp, ip)
	if err != nil {
		t.Fatal(err)
	}
	err = AttachTransportCoordinates(res, []*rtp.Packet{mkPacket(1, 1)})
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeIndexMissing {
		t.Fatalf("want INDEX_MISSING, got %v", err)
	}
}

func TestReadRTPTruncatedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.rtp")
	// Length says 40 bytes, only 2 follow.
	bad := append([]byte{40, 0}, 0x80, 0x60)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRTPSource(path)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeTruncatedUnit {
		t.Fatalf("want TRUNCATED_UNIT, got %v", err)
	}
}

func TestValidateBoundariesConflict(t *testing.T) {
	res := &ScanResult{
		Units: []Unit{{End: 100}},
		Boundaries: []BoundaryEvent{
			{Offset: 10, Length: 2, Key: true},
			{Offset: 10, Length: 2, Key: true},
		},
	}
	err := ValidateBoundaries(res)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeBoundaryConflict || pe.Offset != 10 {
		t.Fatalf("want BOUNDARY_CONFLICT at 10, got %v", err)
	}
}
