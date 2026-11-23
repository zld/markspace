package probe

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/model"
)

// ---- tiny in-test container builders (no external files) ----

func box(typ string, payload []byte, children ...[]byte) []byte {
	body := payload
	for _, c := range children {
		body = append(body, c...)
	}
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body)))
	copy(hdr[4:], []byte(typ))
	return append(hdr, body...)
}

func tkhd(id uint32) []byte {
	pl := make([]byte, 96) // version0 tkhd
	binary.BigEndian.PutUint32(pl[12:], id)
	// identity matrix at +40
	binary.BigEndian.PutUint32(pl[40:], 1<<16)
	binary.BigEndian.PutUint32(pl[56:], 1<<16)
	return box("tkhd", pl)
}

func mdhd(scale uint32, dur uint32) []byte {
	pl := make([]byte, 32)
	binary.BigEndian.PutUint32(pl[12:], scale)
	binary.BigEndian.PutUint32(pl[16:], dur)
	return box("mdhd", pl)
}

func hdlr(kind string) []byte {
	pl := make([]byte, 24)
	copy(pl[8:], []byte(kind))
	return box("hdlr", pl)
}

func nclx(prim, trc, mtx uint16) []byte {
	pl := make([]byte, 12)
	copy(pl[0:4], []byte("nclx"))
	binary.BigEndian.PutUint16(pl[4:], prim)
	binary.BigEndian.PutUint16(pl[6:], trc)
	binary.BigEndian.PutUint16(pl[8:], mtx)
	return box("colr", pl)
}

func stsz(n uint32) []byte {
	pl := make([]byte, 12)
	binary.BigEndian.PutUint32(pl[8:], n)
	return box("stsz", pl)
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanMP4HappyPath(t *testing.T) {
	trak := box("trak", nil,
		tkhd(7), mdhd(90000, 180000), hdlr("vide"), nclx(9, 16, 9), stsz(100))
	moov := box("moov", nil, trak)
	ftyp := box("ftyp", append([]byte("isom"), make([]byte, 4)...))
	data := append(ftyp, moov...)
	p := writeFile(t, "a.mp4", data)

	meta, err := Scan(p, DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if meta.Format != "mp4" || len(meta.Tracks) != 1 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	tr := meta.Tracks[0]
	if tr.ID != 7 || tr.TimeScale != 90000 || tr.DurationTS != 180000 || tr.SampleCount != 100 {
		t.Fatalf("track parse wrong: %+v", tr)
	}
	if tr.Kind != KindVideo || tr.Rotation == nil || tr.Rotation.Value.(int64) != 0 {
		t.Fatalf("handler/rotation wrong: %+v rot=%v", tr.Kind, tr.Rotation)
	}
	nc := NormalizeColor(tr.Color)
	if nc.Primaries != 9 || nc.Transfer != 16 || nc.Matrix != 9 {
		t.Fatalf("color normalize wrong: %+v", nc)
	}
	if meta.SHA256 == "" || meta.ScanVer != ScanVersion {
		t.Fatal("missing digest/scan version")
	}
}

func TestScanMP4TruncatedBox(t *testing.T) {
	good := box("moov", nil, box("trak", nil, tkhd(1), mdhd(90000, 10), hdlr("vide")))
	// Declare a box larger than the file.
	bad := make([]byte, len(good))
	copy(bad, good)
	binary.BigEndian.PutUint32(bad, uint32(len(good)+500))
	p := writeFile(t, "trunc.mp4", bad)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || pe.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncated_box, got %v", err)
	}
	if pe.ByteRange.Offset != 0 {
		t.Fatalf("error must point at box header offset 0, got %d", pe.ByteRange.Offset)
	}
}

func TestScanMP4IllegalLength(t *testing.T) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:], 4) // size smaller than the header itself
	copy(hdr[4:], []byte("moov"))
	p := writeFile(t, "illegal.mp4", hdr)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || (pe.Code != model.CodeIllegalLength && pe.Code != model.CodeTruncatedBox) {
		t.Fatalf("want illegal_length or truncated_box, got %v", err)
	}
}

func TestScanMP4MissingTrack(t *testing.T) {
	data := box("ftyp", make([]byte, 8))
	p := writeFile(t, "empty.mp4", data)
	_, err := Scan(p, DefaultLimits())
	pe, ok := firstPipelineErr(t, err)
	if !ok || pe.Code != model.CodeMissingTrack {
		t.Fatalf("want missing_track, got %v", err)
	}
}

func TestColorConflictPriority(t *testing.T) {
	ci := ColorInfo{
		Primaries: []SourcedValue{
			{Source: "a", Value: int64(0)}, {Source: "b", Value: int64(9)},
		},
		ICCProfile: []SourcedValue{{Source: "colr/prof", Value: "prof"}},
	}
	nc := NormalizeColor(ci)
	if nc.Primaries != 9 {
		t.Fatalf("non-zero declaration must win over 0: %+v", nc)
	}
	if len(nc.Sources) < 2 || nc.Conflicts == nil {
		t.Fatalf("sources/conflicts must be recorded: %+v", nc)
	}
}

func firstPipelineErr(t *testing.T, err error) (*model.PipelineError, bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	pe, ok := model.AsPipeline(err)
	return pe, ok
}
