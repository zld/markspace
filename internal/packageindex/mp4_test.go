package packageindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"example.com/mediavault/internal/fault"
)

// sampleMP4 builds a tiny but structurally valid wrapper with one PCM payload.
func sampleMP4(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	spec := ArchiveSpec{
		SampleRate:    8000,
		Channels:      1,
		BitsPerSample: 16,
		SampleCount:   1,
		Duration:      uint64(len(payload) / 2),
		PayloadSize:   int64(len(payload)),
		ChunkOffsets:  []int64{0},
		ChunkFrames:   []int64{int64(len(payload) / 2)},
	}
	if _, err := WriteArchive(&buf, spec, bytes.NewReader(payload)); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return buf.Bytes()
}

func TestScanArchiveRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0x11, 0x22}, 512)
	raw := sampleMP4(t, payload)
	idx, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if idx.MdatBytes != int64(len(payload)) {
		t.Fatalf("mdat bytes = %d, want %d", idx.MdatBytes, len(payload))
	}
	if len(idx.ContainerDigest) != 64 || len(idx.PayloadDigest) != 64 {
		t.Fatalf("digests: %q %q", idx.ContainerDigest, idx.PayloadDigest)
	}
	if idx.Timescale != 8000 {
		t.Fatalf("timescale = %d", idx.Timescale)
	}
	if err := ValidateChunkOffsets(bytes.NewReader(raw), idx); err != nil {
		t.Fatalf("chunk offsets: %v", err)
	}
}

func TestContainerDigestTracksWrapperChanges(t *testing.T) {
	payload := bytes.Repeat([]byte{0x33}, 1024)
	a := sampleMP4(t, payload)

	// Rebuild with a different timescale: payload identical, wrapper changed.
	var buf bytes.Buffer
	spec := ArchiveSpec{
		SampleRate: 8000, Channels: 1, BitsPerSample: 16, SampleCount: 1,
		Timescale: 9000, Duration: 512, PayloadSize: int64(len(payload)),
		ChunkOffsets: []int64{0}, ChunkFrames: []int64{512},
	}
	if _, err := WriteArchive(&buf, spec, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()

	ia, err := ScanReader(bytes.NewReader(a), int64(len(a)))
	if err != nil {
		t.Fatal(err)
	}
	ib, err := ScanReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if ia.PayloadDigest != ib.PayloadDigest {
		t.Fatal("identical payload must have an identical payload digest")
	}
	if ia.ContainerDigest == ib.ContainerDigest {
		t.Fatal("a wrapper change must change the container digest")
	}
}

func TestScanRejectsBoxBeyondEOF(t *testing.T) {
	raw := []byte{
		0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', '6', 0, 0, 0, 0, 'i', 's', 'o', '6', 'm', 'p', '4', '1',
		0xFF, 0xFF, 0xFF, 0x00, 'm', 'd', 'a', 't',
	}
	_, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if fault.CodeOf(err) != fault.CodeMP4Extent {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if fault.StageOf(err) != fault.StageScanned {
		t.Fatalf("stage = %q", fault.StageOf(err))
	}
	if fault.OffsetOf(err) != 24 {
		t.Fatalf("offset = %d", fault.OffsetOf(err))
	}
}

func TestScanRejectsZeroLengthBox(t *testing.T) {
	// size 0 means "extends to EOF" for a final box, so use a 4-byte body.
	raw := []byte{0, 0, 0, 4, 'f', 'r', 'e', 'e'}
	_, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if err == nil {
		t.Fatal("a box smaller than its header was accepted")
	}
	if fault.CodeOf(err) != fault.CodeMP4Box {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestScanRejectsDeepNesting(t *testing.T) {
	// Build MaxDepth+2 nested container boxes, each holding the next.
	inner := []byte{}
	for i := 0; i < MaxDepth+2; i++ {
		size := uint32(8 + len(inner))
		hdr := make([]byte, 8)
		binary.BigEndian.PutUint32(hdr[:4], size)
		copy(hdr[4:], []byte("moov"))
		inner = append(hdr, inner...)
	}
	_, err := ScanReader(bytes.NewReader(inner), int64(len(inner)))
	if err == nil {
		t.Fatal("over-deep nesting accepted")
	}
	if fault.CodeOf(err) != fault.CodeMP4Box {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestScanRejectsTrailingGarbage(t *testing.T) {
	payload := bytes.Repeat([]byte{0x44}, 256)
	raw := append(sampleMP4(t, payload), 0xDE, 0xAD, 0xBE, 0xEF)
	_, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if fault.CodeOf(err) != fault.CodeMP4Extent {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestValidateChunkOffsetsRejectsOffsetOutsideMdat(t *testing.T) {
	payload := bytes.Repeat([]byte{0x55}, 256)
	raw := sampleMP4(t, payload)
	idx, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// Point the single chunk offset far outside every mdat.
	var stco *BoxEntry
	for i := range idx.Boxes {
		if idx.Boxes[i].Type == "stco" {
			stco = &idx.Boxes[i]
			break
		}
	}
	if stco == nil {
		t.Fatal("no stco box")
	}
	// stco layout: 8 header + 4 fullbox + 4 entry_count + 4 first offset.
	off := int(stco.Offset) + 8 + 4 + 4
	binary.BigEndian.PutUint32(raw[off:], 0x7FFF_FFFF)
	idx2, err := ScanReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateChunkOffsets(bytes.NewReader(raw), idx2); fault.CodeOf(err) != fault.CodeMP4Extent {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestScanFileReadsFromDisk(t *testing.T) {
	payload := bytes.Repeat([]byte{0x66}, 128)
	raw := sampleMP4(t, payload)
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(path, raw, 0o444); err != nil {
		t.Fatal(err)
	}
	idx, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	if idx.FileSize != int64(len(raw)) {
		t.Fatalf("file size = %d", idx.FileSize)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := ValidateChunkOffsets(f, idx); err != nil {
		t.Fatal(err)
	}
}

func TestWriteArchiveIsDeterministic(t *testing.T) {
	payload := bytes.Repeat([]byte{0x77}, 300)
	a := sampleMP4(t, payload)
	b := sampleMP4(t, payload)
	if !bytes.Equal(a, b) {
		t.Fatal("archive writer is not deterministic")
	}
	sum := sha256.Sum256(a)
	if len(sum) != 32 {
		t.Fatal("unreachable")
	}
}

func TestWriteArchiveRejectsOversizedPayload(t *testing.T) {
	spec := ArchiveSpec{PayloadSize: 3 << 30}
	_, err := WriteArchive(io.Discard, spec, bytes.NewReader(nil))
	if fault.CodeOf(err) != fault.CodeLimit {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}
