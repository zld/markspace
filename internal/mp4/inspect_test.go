package mp4

import (
	"bytes"
	"io"
	"strings"
	"testing"

	go_mp4 "github.com/abema/go-mp4"
)

// writeSeekBuffer is an in-memory io.WriteSeeker for go-mp4's Writer.
type writeSeekBuffer struct {
	buf []byte
	pos int64
}

func (w *writeSeekBuffer) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.buf)) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(len(w.buf)) + offset
	}
	return w.pos, nil
}

// buildMP4 writes a minimal ftyp+moov(mvhd/trak/mdia/mdhd) container
// with controllable brand and timescale, entirely in memory.
func buildMP4(major string, minor uint32, timescale uint32, duration uint64) []byte {
	sink := &writeSeekBuffer{}
	w := go_mp4.NewWriter(sink)

	bi, _ := w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeFtyp()})
	brand := [4]byte{}
	copy(brand[:], major)
	go_mp4.Marshal(w, &go_mp4.Ftyp{
		MajorBrand:   brand,
		MinorVersion: minor,
		CompatibleBrands: []go_mp4.CompatibleBrandElem{
			{CompatibleBrand: brand},
		},
	}, go_mp4.Context{})
	w.EndBox()

	_, _ = w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMoov()})
	mbi, _ := w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMvhd()})
	mvhd := &go_mp4.Mvhd{Timescale: timescale}
	mvhd.DurationV0 = uint32(duration)
	go_mp4.Marshal(w, mvhd, go_mp4.Context{})
	w.EndBox()

	_, _ = w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeTrak()})
	_, _ = w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMdia()})
	_, _ = w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMdhd()})
	mdhd := &go_mp4.Mdhd{Timescale: timescale}
	mdhd.DurationV0 = uint32(duration)
	go_mp4.Marshal(w, mdhd, go_mp4.Context{})
	w.EndBox() // mdhd
	w.EndBox() // mdia
	w.EndBox() // trak
	w.EndBox() // moov
	_ = bi
	_ = mbi
	return sink.buf
}

func TestInspectExtractsMetadata(t *testing.T) {
	raw := buildMP4("isom", 1, 48000, 24000)
	md, err := Inspect(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if md.Brand != "isom" || md.MinorVersion != 1 || md.Timescale != 48000 || md.Duration != 24000 {
		t.Fatalf("metadata wrong: %+v", md)
	}
	joined := strings.Join(md.BoxSignature, ",")
	for _, want := range []string{"ftyp", "moov", "mvhd", "trak", "mdia", "mdhd"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("signature missing %s: %s", want, joined)
		}
	}
}

func TestContainerDigestChangesWithEncapsulationOnly(t *testing.T) {
	base := buildMP4("isom", 1, 48000, 24000)
	md1, _ := Inspect(bytes.NewReader(base))
	h1 := Digest(md1)

	// Same duration/timescale, different brand and minor version: this is
	// a pure container change with no PCM involvement.
	other := buildMP4("mp42", 2, 48000, 24000)
	md2, _ := Inspect(bytes.NewReader(other))
	h2 := Digest(md2)
	if h1 == h2 {
		t.Fatal("container digest must change when encapsulation changes")
	}

	// Re-inspecting identical bytes must reproduce the digest.
	md3, _ := Inspect(bytes.NewReader(base))
	if Digest(md3) != h1 {
		t.Fatal("container digest not reproducible")
	}
}

func TestInspectRejectsGarbage(t *testing.T) {
	if _, err := Inspect(bytes.NewReader([]byte("not an mp4 file at all!!"))); err == nil {
		t.Fatal("garbage input must fail inspection")
	}
}
