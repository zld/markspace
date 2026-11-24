package pipeline

import (
	"bytes"
	"testing"

	gomp4 "github.com/abema/go-mp4"
)

func TestFragmentWriterRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	seeker := &writeSeeker{buf: &buf}
	fw := NewFragmentWriter(seeker, 1)
	if err := fw.WriteInitSegment(48000); err != nil {
		t.Fatal(err)
	}
	samples := [][]byte{
		{1, 2, 3, 4},
		{5, 6, 7, 8, 9},
		{10, 11},
	}
	if err := fw.WriteFragment(1, samples, 480); err != nil {
		t.Fatal(err)
	}
	if err := fw.WriteFragment(2, [][]byte{{0xAA, 0xBB}}, 480); err != nil {
		t.Fatal(err)
	}

	r := bytes.NewReader(buf.Bytes())
	// Top-level boxes must appear in fragmented-MP4 order.
	infos, err := gomp4.ExtractBoxes(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeFtyp()},
		{gomp4.BoxTypeMoov()},
		{gomp4.BoxTypeMoof()},
		{gomp4.BoxTypeMdat()},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, bi := range infos {
		got[bi.Type.String()]++
	}
	for _, want := range []string{"ftyp", "moov", "moof", "mdat"} {
		if got[want] == 0 {
			t.Fatalf("missing top-level box %s, got %v", want, got)
		}
	}
	if got["moof"] != 2 || got["mdat"] != 2 {
		t.Fatalf("want two fragments, got moof=%d mdat=%d", got["moof"], got["mdat"])
	}

	// Verify the trun data offset of the first fragment points at the first
	// mdat payload byte and that the sample bytes are readable there.
	moof := infos[2]
	truns, err := gomp4.ExtractBoxes(r, moof, []gomp4.BoxPath{
		{gomp4.BoxTypeTraf(), gomp4.BoxTypeTrun()},
	})
	if err != nil || len(truns) != 1 {
		t.Fatalf("trun extraction: %d err=%v", len(truns), err)
	}
	mdats, _ := gomp4.ExtractBoxes(r, nil, []gomp4.BoxPath{{gomp4.BoxTypeMdat()}})
	mdat := mdats[0]
	// Payload starts after the mdat 8-byte header.
	payloadOff := int64(mdat.Offset) + int64(mdat.HeaderSize)
	first := make([]byte, 4)
	if _, err := r.ReadAt(first, payloadOff); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, samples[0]) {
		t.Fatalf("mdat payload=%v want %v", first, samples[0])
	}
	// The recorded data offset must equal payload position minus moof start.
	trunPayload, err := readTrunDataOffset(r, truns[0])
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := uint32(payloadOff - int64(moof.Offset))
	if trunPayload != wantOffset {
		t.Fatalf("trun data_offset=%d want %d", trunPayload, wantOffset)
	}
}

func readTrunDataOffset(r *bytes.Reader, trun *gomp4.BoxInfo) (uint32, error) {
	// full-box header (4) + sample_count (4) precede data_offset.
	pos := int64(trun.Offset) + int64(trun.HeaderSize) + 8
	buf := make([]byte, 4)
	if _, err := r.ReadAt(buf, pos); err != nil {
		return 0, err
	}
	return uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3]), nil
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker for deterministic tests.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(w.buf.Len()) {
		need := end - int64(w.buf.Len())
		w.buf.Write(make([]byte, need))
	}
	copy(w.buf.Bytes()[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		w.pos = offset
	case 1:
		w.pos += offset
	case 2:
		w.pos = int64(w.buf.Len()) + offset
	}
	return w.pos, nil
}
