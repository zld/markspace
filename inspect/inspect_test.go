package inspect

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"streamcheck/core"
)

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func fullbox(typ string, version byte, payload []byte) []byte {
	p := make([]byte, 4+len(payload))
	p[0] = version
	copy(p[4:], payload)
	return box(typ, p)
}

func nal(typ byte, payload []byte) []byte {
	b := make([]byte, 4+1+len(payload))
	binary.BigEndian.PutUint32(b, uint32(1+len(payload)))
	b[4] = typ
	copy(b[5:], payload)
	return b
}

func moof(seq uint32, decodeTime uint64) []byte {
	mfhd := make([]byte, 4)
	binary.BigEndian.PutUint32(mfhd, seq)
	tfhd := make([]byte, 4)
	binary.BigEndian.PutUint32(tfhd, 1)
	tfdt := make([]byte, 8)
	binary.BigEndian.PutUint64(tfdt, decodeTime)
	traf := box("traf", append(fullbox("tfhd", 0, tfhd), fullbox("tfdt", 1, tfdt)...))
	return box("moof", append(fullbox("mfhd", 0, mfhd), traf...))
}

// buildFMP4 writes a two-segment fMP4: seg0 IDR at t=0, seg1 IDR at t=2000.
func buildFMP4(t *testing.T, corruptLen bool) (path string, corruptAt int64) {
	t.Helper()
	var out []byte
	out = append(out, box("ftyp", []byte("isom0000"))...)
	out = append(out, box("moov", nil)...)
	out = append(out, moof(1, 0)...)
	out = append(out, box("mdat", nal(5, []byte{0x88, 0x99}))...)
	out = append(out, moof(2, 2000)...)
	payload := nal(5, []byte{0xaa})
	if corruptLen {
		// First NAL fine, second declares a length past the buffer end.
		payload = append(payload, 0x00, 0x00, 0x7f, 0xff, 0x65)
	} else {
		payload = append(payload, nal(1, []byte{0x01})...)
	}
	mdatOff := int64(len(out)) + 8
	out = append(out, box("mdat", payload)...)
	path = filepath.Join(t.TempDir(), "test.mp4")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if corruptLen {
		corruptAt = mdatOff + int64(len(nal(5, []byte{0xaa})))
	}
	return path, corruptAt
}

func TestInspectMP4Structure(t *testing.T) {
	path, _ := buildFMP4(t, false)
	rep, err := InspectMP4(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 2 {
		t.Fatalf("segments: %+v", rep.Segments)
	}
	if rep.Segments[0].DecodeTime.Cmp(core.FromMillis(0)) != 0 ||
		rep.Segments[1].DecodeTime.Cmp(core.FromMillis(2000)) != 0 {
		t.Fatalf("decode times: %+v", rep.Segments)
	}
	if rep.Segments[0].MoofOffset <= 0 || rep.Segments[0].MdatSize <= 0 {
		t.Fatalf("offsets: %+v", rep.Segments[0])
	}
}

func TestInspectMP4CorruptOffsetExact(t *testing.T) {
	path, want := buildFMP4(t, true)
	rep, err := InspectMP4(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seg := rep.Segments[1]
	payload := make([]byte, seg.MdatSize)
	if _, err := f.ReadAt(payload, seg.MdatOffset); err != nil {
		t.Fatal(err)
	}
	seg.AnalyzeAVC(payload)
	if !seg.Corrupt {
		t.Fatal("corruption not detected")
	}
	if seg.CorruptOffset != want {
		t.Fatalf("offset error: got %d want %d", seg.CorruptOffset, want)
	}
	if !seg.Keyframe {
		t.Fatal("IDR before corruption must still mark keyframe")
	}
}

func TestInspectMP4TruncatedBox(t *testing.T) {
	var out []byte
	out = append(out, box("ftyp", []byte("isom0000"))...)
	// mdat declares 100 bytes but the file ends after the header.
	out = append(out, 0, 0, 0, 100, 'm', 'd', 'a', 't', 0x65)
	path := filepath.Join(t.TempDir(), "trunc.mp4")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InspectMP4(path, 1000)
	if err == nil {
		t.Fatal("truncated box must fail")
	}
	e, ok := err.(*core.Error)
	if !ok || e.Kind != core.KindInspect || e.Offset < 0 {
		t.Fatalf("structured error: %v", err)
	}
}

func TestScanAVC(t *testing.T) {
	data := append(nal(5, []byte{1}), nal(1, []byte{2, 3})...)
	nals, corrupt := ScanAVC(data, 100)
	if corrupt != -1 || len(nals) != 2 || !nals[0].IDR || nals[1].IDR {
		t.Fatalf("nals=%+v corrupt=%d", nals, corrupt)
	}
	if nals[1].Offset != 100+int64(len(nal(5, []byte{1}))) {
		t.Fatalf("offset: %d", nals[1].Offset)
	}
	if _, c := ScanAVC([]byte{0, 0}, 0); c != 0 {
		t.Fatalf("trailing bytes corrupt at 0, got %d", c)
	}
}

func TestGOPAndCompare(t *testing.T) {
	rep := GOP([]int64{0, 2000, 6000, 8000})
	if rep.MaxIntervalMs != 4000 {
		t.Fatalf("max interval: %+v", rep)
	}
	if CompareKeyframes([]int64{0, 2000}, []int64{0, 2000}) != -1 {
		t.Fatal("equal keyframes")
	}
	if CompareKeyframes([]int64{0, 2000}, []int64{0, 4000}) != 1 {
		t.Fatal("first mismatch at 1")
	}
	if CompareKeyframes([]int64{0}, []int64{0, 2000}) != 1 {
		t.Fatal("length mismatch")
	}
}
