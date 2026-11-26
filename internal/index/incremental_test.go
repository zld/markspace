package index

import (
	"os"
	"path/filepath"
	"testing"

	"tsindex/internal/digest"
	"tsindex/internal/ingest"
)

func TestTwoTierDigestDistinguishesContentFromEncapsulation(t *testing.T) {
	// Two packets with identical payload but different headers (CC changed):
	// content hash must match while the raw hash must differ.
	p1 := ingestPacket(0x100, 0, []byte{0xDE, 0xAD})
	p2 := ingestPacket(0x100, 3, []byte{0xDE, 0xAD})

	h1 := digest.NewHasher()
	h1.AddPacket(p1.Raw[:], p1.Payload())
	h2 := digest.NewHasher()
	h2.AddPacket(p2.Raw[:], p2.Payload())
	d1, d2 := h1.Sum(), h2.Sum()

	if d1.Content != d2.Content {
		t.Fatal("identical demuxed content must hash equal")
	}
	if d1.Raw == d2.Raw {
		t.Fatal("encapsulation change must change the raw transport hash")
	}
	if !d1.ContentEqual(d2) || d1.Equal(d2) {
		t.Fatal("digest relation helpers disagree")
	}
}

func TestKeyframeScannerH264AndHEVC(t *testing.T) {
	epoch := &ingest.MappingEpoch{Programs: map[uint16]*ingest.ProgramMapping{
		1: {ProgramNumber: 1, Streams: map[uint16]ingest.StreamInfo{
			0x100: {Codec: ingest.CodecH264ForTest(), Complete: true},
			0x200: {Codec: ingest.CodecHEVCForTest(), Complete: true},
		}},
	}}
	sc := NewScanner(epoch)

	// H.264 IDR NAL (type 5) behind an Annex-B start code.
	h264IDR := pesPacket(0x100, true, 0, append([]byte{0, 0, 0, 1}, 0x65 /*IDR*/, 0xAA))
	kf := sc.Feed(h264IDR)
	if kf == nil || !kf.Complete || kf.Codec != CodecH264 || kf.Program != 1 {
		t.Fatalf("H.264 IDR not detected: %+v", kf)
	}
	// H.264 non-IDR P slice (type 1) must not be a keyframe.
	h264P := pesPacket(0x100, true, 1, append([]byte{0, 0, 1}, 0x41 /*P*/, 0xBB))
	if sc.Feed(h264P) != nil {
		t.Fatal("H.264 P-slice wrongly indexed as keyframe")
	}
	// HEVC IDR_W_RADL (type 19): header byte = 19<<1.
	hevcIDR := pesPacket(0x200, true, 0, append([]byte{0, 0, 1}, byte(19<<1), 0xCC))
	kf = sc.Feed(hevcIDR)
	if kf == nil || kf.Codec != CodecHEVC || !kf.Complete {
		t.Fatalf("HEVC IDR not detected: %+v", kf)
	}
	if len(sc.Frames()) != 2 {
		t.Fatalf("want 2 keyframes, got %d", len(sc.Frames()))
	}
}

func TestUnsupportedCodecProducesIncompleteEntry(t *testing.T) {
	epoch := &ingest.MappingEpoch{Programs: map[uint16]*ingest.ProgramMapping{
		1: {ProgramNumber: 1, Streams: map[uint16]ingest.StreamInfo{
			0x300: {Codec: "other", Complete: false},
		}},
	}}
	sc := NewScanner(epoch)
	kf := sc.Feed(pesPacket(0x300, true, 0, []byte{0, 0, 1, 0x99}))
	if kf == nil || kf.Complete {
		t.Fatalf("unsupported codec must yield incomplete marker: %+v", kf)
	}
}

func TestSidecarRoundTripAndTruncation(t *testing.T) {
	dir := t.TempDir()
	rec := SegmentRecord{
		SegmentID: 7, StartOffset: 1880, EndOffset: 3760, Packets: 10,
		Keyframes: []Keyframe{{Program: 1, TrackPID: 0x100, Codec: CodecH264,
			Ticks: 27_000_000, SourceOffset: 1880, Complete: true}},
	}
	copy(rec.RawHash[:], bytesRange(1))
	copy(rec.ContentHash[:], bytesRange(9))

	path, err := WriteSidecar(dir, "asset.ts", rec)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "asset.ts.tsix" {
		t.Fatalf("sidecar naming wrong: %s", path)
	}
	got, err := ReadSidecar(path)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if got.SegmentID != 7 || got.Packets != 10 || got.RawHash != rec.RawHash ||
		got.ContentHash != rec.ContentHash || len(got.Keyframes) != 1 ||
		got.Keyframes[0].SourceOffset != 1880 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// Truncate the sidecar: every short read must surface as recoverable
	// ErrIncompleteSidecar rather than a silently accepted partial index.
	raw, _ := os.ReadFile(path)
	for _, cut := range []int{len(raw) - 1, len(raw) / 2, 30} {
		bad := filepath.Join(dir, "bad.tsix")
		if err := os.WriteFile(bad, raw[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSidecar(bad); err == nil {
			t.Fatalf("truncated sidecar at %d bytes accepted", cut)
		}
	}
}

func TestSidecarMissingBoxRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.tsix")
	// Valid BMFF size/type header but no ftyp and no uuid payload.
	if err := os.WriteFile(path, []byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSidecar(path); err == nil {
		t.Fatal("sidecar without ftyp/uuid must be rejected")
	}
}

// --- small builders shared with the keyframe tests ---

func ingestPacket(pid uint16, cc uint8, payloadPrefix []byte) *ingest.Record {
	var raw [188]byte
	raw[0] = ingest.SyncByte
	raw[1] = byte(pid >> 8)
	raw[2] = byte(pid)
	raw[3] = 0x10 | (cc & 0x0F)
	copy(raw[4:], payloadPrefix)
	p := ingest.NewRecordForTest(raw, 0, 0)
	return p
}

func pesPacket(pid uint16, pusi bool, cc uint8, es []byte) *ingest.Record {
	var raw [188]byte
	raw[0] = ingest.SyncByte
	if pusi {
		raw[1] = 0x40
	}
	raw[1] |= byte(pid >> 8)
	raw[2] = byte(pid)
	raw[3] = 0x10 | (cc & 0x0F)
	// PES header: 00 00 01 streamid(0xE0) length(2) flags(2) headerlen=0.
	pes := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 0}
	off := 4
	copy(raw[off:], pes)
	off += len(pes)
	copy(raw[off:], es)
	return ingest.NewRecordForTest(raw, 0, 12345)
}

func bytesRange(start byte) []byte {
	out := make([]byte, digest.Size)
	for i := range out {
		out[i] = start + byte(i)
	}
	return out
}
