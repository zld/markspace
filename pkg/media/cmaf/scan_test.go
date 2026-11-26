package cmaf

import (
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/timebase"
)

// buildFragment assembles a minimal but spec-shaped moof+mdat fragment.
// trun flags 0x000300 = sample-duration-present | sample-size-present.
func buildFragment(seq uint32, tfdt uint64, sampleSizes []uint32, sampleDurs []uint32) []byte {
	trun := NewBox(BoxTrun).FullHeader(1, 0x000300).U32(uint32(len(sampleSizes)))
	for i := range sampleSizes {
		trun.U32(sampleDurs[i]).U32(sampleSizes[i])
	}
	tfhd := NewBox(BoxTfhd).FullHeader(0, 0x000008).U32(1).U32(3003)
	tfdtBox := NewBox(BoxTfdt).FullHeader(1, 0).U64(tfdt)
	mfhd := NewBox(BoxMfhd).FullHeader(0, 0).U32(seq)
	traf := NewBox(BoxTraf).Child(tfhd).Child(tfdtBox).Child(trun)
	moof := NewBox(BoxMoof).Child(mfhd).Child(traf)

	payloadLen := 0
	for _, s := range sampleSizes {
		payloadLen += int(s)
	}
	mdat := NewBox(BoxMdat).Bytes(make([]byte, payloadLen))
	return append(moof.Build(), mdat.Build()...)
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFragmentOK(t *testing.T) {
	data := buildFragment(7, 123456, []uint32{100, 200}, []uint32{3003, 3003})
	p := writeTemp(t, "frag_0007.m4s", data)
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Sequence != 7 || f.Tfdt != 123456 || f.TrackID != 1 {
		t.Fatalf("parsed wrong header: %+v", f)
	}
	if len(f.Samples) != 2 || f.Samples[1].Size != 200 || f.Samples[0].Duration != 3003 {
		t.Fatalf("trun samples wrong: %+v", f.Samples)
	}
	if !f.MdatOK || f.MdatGap != 0 {
		t.Fatalf("mdat should be intact: ok=%v gap=%d", f.MdatOK, f.MdatGap)
	}
	if f.TfdtValueLen != 8 || f.TfdtOffset <= f.Moof.Offset {
		t.Fatalf("tfdt patch anchor wrong: off=%d len=%d", f.TfdtOffset, f.TfdtValueLen)
	}
}

func TestMdatTruncationDetected(t *testing.T) {
	data := buildFragment(3, 1000, []uint32{500, 500}, []uint32{3003, 3003})
	// Drop 400 payload bytes from the end.
	trunc := data[:len(data)-400]
	p := writeTemp(t, "trunc.m4s", trunc)
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatalf("partial fragment must still return: %v", err)
	}
	if f.MdatOK {
		t.Fatal("truncated mdat must be flagged")
	}
	if f.MdatGap != 400 {
		t.Fatalf("gap=%d want 400", f.MdatGap)
	}
	found := false
	for _, d := range f.Defects {
		if d.Code == timebase.CodeMdatTruncated && d.Box == BoxMdat && d.ByteOffset >= 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected positioned mdat defect, got %+v", f.Defects)
	}
}

func TestOversizedBoxRejectedWithoutOutOfBoundsRead(t *testing.T) {
	data := buildFragment(1, 0, []uint32{10}, []uint32{100})
	// Corrupt the mdat size field to claim more than the file holds.
	mdatSizeOff := -1
	for off := 0; off+8 <= len(data); {
		size := int(uint32(data[off])<<24 | uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3]))
		if string(data[off+4:off+8]) == BoxMdat {
			mdatSizeOff = off
			break
		}
		off += size
	}
	if mdatSizeOff < 0 {
		t.Fatal("test fragment has no mdat box")
	}
	data[mdatSizeOff+0] = 0x7f
	data[mdatSizeOff+1] = 0xff
	data[mdatSizeOff+2] = 0xff
	data[mdatSizeOff+3] = 0xff
	p := writeTemp(t, "badlen.m4s", data)
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatalf("partial parse should return fragment: %v", err)
	}
	var me *timebase.MediaError
	for _, d := range f.Defects {
		if d.Code == timebase.CodeMdatTruncated && d.Box == BoxMdat && d.ByteOffset >= 0 {
			me = d
		}
	}
	if me == nil {
		t.Fatalf("want positioned E_MDAT_TRUNCATED, got %+v", f.Defects)
	}
	if f.MdatOK || f.MdatGap <= 0 {
		t.Fatalf("mdat must be marked truncated: ok=%v gap=%d", f.MdatOK, f.MdatGap)
	}
}

func TestTruncatedHeaderRejected(t *testing.T) {
	p := writeTemp(t, "tiny.m4s", []byte{0x00, 0x00, 0x00, 0x20, 'm', 'd', 'a'})
	_, err := ParseFragment(p)
	me := timebase.AsMedia(err)
	if me == nil || me.Code != timebase.CodeReadOutOfBounds {
		t.Fatalf("want E_READ_OUT_OF_BOUNDS, got %+v", me)
	}
}

func TestContainerTrailingBytesRejected(t *testing.T) {
	// moof with a declared size larger than its real children.
	mfhd := NewBox(BoxMfhd).FullHeader(0, 0).U32(1)
	moof := NewBox(BoxMoof).Child(mfhd)
	raw := moof.Build()
	// Inflate moof size by 8 and append 8 junk bytes inside it.
	raw[0] = 0x00
	raw[1] = 0x00
	raw[2] = byte((len(raw) + 8) >> 8)
	raw[3] = byte(len(raw) + 8)
	raw = append(raw, []byte("JUNKJUNK")...)
	mdat := NewBox(BoxMdat).Bytes([]byte{1, 2, 3, 4})
	raw = append(raw, mdat.Build()...)
	p := writeTemp(t, "trail.m4s", raw)
	_, err := ParseFragment(p)
	me := timebase.AsMedia(err)
	if me == nil || (me.Code != timebase.CodeReadOutOfBounds && me.Code != timebase.CodeBoxLength) {
		t.Fatalf("want structural defect for trailing bytes, got %+v", me)
	}
}

func TestParseInitExtractsTimescaleAndTrex(t *testing.T) {
	mdhd := NewBox("mdhd").FullHeader(0, 0).U32(0).U32(0).U32(90000).U32(0).Bytes([]byte{0, 0, 0, 0, 0, 0})
	tkhd := NewBox("tkhd").FullHeader(0, 0).U32(0).U32(0).U32(1).Bytes(make([]byte, 4))
	mdia := NewBox("mdia").Child(mdhd)
	trak := NewBox("trak").Child(tkhd).Child(mdia)
	trex := NewBox("trex").FullHeader(0, 0).U32(1).U32(1).U32(3003).U32(0).U32(0)
	mvex := NewBox("mvex").Child(trex)
	moov := NewBox(BoxMoov).Child(trak).Child(mvex)
	ftyp := NewBox(BoxFtyp).Bytes([]byte("iso5dash"))
	init := append(ftyp.Build(), moov.Build()...)
	p := writeTemp(t, "init.mp4", init)
	info, err := ParseInit(p)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	if info.Timescale != 90000 || info.TrackID != 1 || info.TrexDefaultDuration != 3003 {
		t.Fatalf("init info wrong: %+v", info)
	}
}

func TestBoxHeaderEndSemantics(t *testing.T) {
	h := BoxHeader{Offset: 10, Size: 24, HeaderSize: 8, PayloadOff: 18, PayloadLen: 16}
	if h.End(100) != 34 {
		t.Fatalf("End = %d want 34", h.End(100))
	}
	eof := BoxHeader{Offset: 10, Size: -1, PayloadOff: 18, PayloadLen: -1}
	if eof.End(55) != 55 {
		t.Fatalf("EOF box End = %d want 55", eof.End(55))
	}
}
