package index

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// mkBox writes a small ISO-BMFF box. Sizes stay under 2^32 so the 8-byte
// header form is used.
func mkBox(typ string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	return append(out, payload...)
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFingerprintSeparatesContainerAndMetadata(t *testing.T) {
	dir := t.TempDir()
	ftyp := mkBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	moov := mkBox("moov", bytes.Repeat([]byte{0x01}, 32))
	mdat := mkBox("mdat", bytes.Repeat([]byte{0xAA}, 64))
	udta := mkBox("udta", []byte("chapter-body"))

	a := writeFile(t, dir, "a.mp4", concat(ftyp, moov, udta, mdat))

	// Metadata-only change: udta payload differs, everything else identical.
	udta2 := mkBox("udta", []byte("CHAPTER-body"))
	b := writeFile(t, dir, "b.mp4", concat(ftyp, moov, udta2, mdat))

	ra, _, err := ScanBoxRegions(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, _, err := ScanBoxRegions(b)
	if err != nil {
		t.Fatal(err)
	}
	fa, err := ComputeFingerprint(a, ra)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := ComputeFingerprint(b, rb)
	if err != nil {
		t.Fatal(err)
	}
	plan := fb.Plan(fa)
	if plan.ContentSame {
		t.Fatal("content digest should differ")
	}
	if !plan.ContainerSame {
		t.Fatal("container digest must stay the same for a udta edit")
	}
	if plan.MetadataSame {
		t.Fatal("metadata digest should differ")
	}
	if !plan.IndexReusable() {
		t.Fatal("a metadata-only edit must keep the sample index reusable")
	}
	if plan.MetadataReusable() {
		t.Fatal("a metadata-only edit must invalidate the metadata cache")
	}
}

func TestFingerprintContainerChangeInvalidatesIndex(t *testing.T) {
	dir := t.TempDir()
	ftyp := mkBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	mdat := mkBox("mdat", bytes.Repeat([]byte{0xBB}, 32))
	a := writeFile(t, dir, "a.mp4", concat(ftyp, mkBox("moov", bytes.Repeat([]byte{1}, 16)), mdat))
	// A remux that inserts another box moves mdat and changes the box tree.
	b := writeFile(t, dir, "b.mp4", concat(ftyp, mkBox("free", []byte("pad")), mkBox("moov", bytes.Repeat([]byte{1}, 16)), mdat))

	fa := mustFingerprint(t, a)
	fb := mustFingerprint(t, b)
	plan := fb.Plan(fa)
	if plan.ContentSame || plan.ContainerSame {
		t.Fatalf("container edit should change container digest: %+v", plan)
	}
	if plan.IndexReusable() {
		t.Fatal("container change must invalidate the parse index")
	}
}

func TestFingerprintSamePathReplacement(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "same.mp4")
	orig := concat(mkBox("ftyp", []byte("isom")), mkBox("mdat", []byte("first payload")))
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	before := mustFingerprint(t, p)
	// Replacement at the same path with different media bytes, same container.
	repl := concat(mkBox("ftyp", []byte("isom")), mkBox("mdat", []byte("other payload")))
	if err := os.WriteFile(p, repl, 0o644); err != nil {
		t.Fatal(err)
	}
	after := mustFingerprint(t, p)
	plan := after.Plan(before)
	if plan.ContentSame {
		t.Fatal("content digest must notice same-path replacement")
	}
	if !plan.ContainerSame {
		t.Fatal("container layout is unchanged, so the index may be reused")
	}
}

func TestFingerprintIdenticalBytesFullyReusable(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "x.mp4", concat(mkBox("ftyp", []byte("isom")), mkBox("mdat", []byte("payload"))))
	f := mustFingerprint(t, p)
	plan := f.Plan(f)
	if !plan.FullReuse() || !plan.IndexReusable() || !plan.MetadataReusable() {
		t.Fatalf("identical fingerprint should be full reuse: %+v", plan)
	}
}

func TestScanBoxRegionsRejectsTruncatedBox(t *testing.T) {
	dir := t.TempDir()
	// Header claims 100 bytes but only 12 are present.
	bad := make([]byte, 12)
	binary.BigEndian.PutUint32(bad[:4], 100)
	copy(bad[4:8], "mdat")
	p := writeFile(t, dir, "trunc.mp4", bad)
	_, _, err := ScanBoxRegions(p)
	if err == nil {
		t.Fatal("truncated box must be reported")
	}
}

func TestScanBoxRegionsClassifiesBoxes(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mix.mp4", concat(
		mkBox("ftyp", []byte("isom")),
		mkBox("moov", bytes.Repeat([]byte{1}, 8)),
		mkBox("emsg", []byte("timed")),
		mkBox("mdat", bytes.Repeat([]byte{2}, 8)),
	))
	regions, boxes, err := ScanBoxRegions(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 4 {
		t.Fatalf("boxes = %d", len(boxes))
	}
	want := map[string]Class{"ftyp": ClassContainer, "moov": ClassContainer, "emsg": ClassMetadata, "mdat": ClassMedia}
	for _, b := range boxes {
		if got := classAt(regions, b.offset+8); got != want[b.typ] {
			t.Errorf("box %s class = %d, want %d", b.typ, got, want[b.typ])
		}
	}
}

func classAt(regions []Region, off int64) Class {
	for _, r := range regions {
		if r.Offset == off {
			return r.Class
		}
	}
	return 0
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mustFingerprint(t *testing.T, path string) Fingerprint {
	t.Helper()
	regions, _, err := ScanBoxRegions(path)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := ComputeFingerprint(path, regions)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}
