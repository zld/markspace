package cmaf

import (
	"encoding/binary"
	"errors"
	"testing"
)

func fullBox(typ string, version byte, flags uint32, body []byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(hdr, body...))
}

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func synthInit(t *testing.T, timescale uint32) []byte {
	t.Helper()
	mdhd := fullBox("mdhd", 0, 0,
		append(make([]byte, 8), u32b(timescale)...)) // 2x u32 dates then timescale
	mdia := box("mdia", mdhd)
	trak := box("trak", mdia)
	moov := box("moov", trak)
	return append(box("ftyp", []byte("dash")), moov...)
}

// synthFragment builds a moof+mdat with samples of the given durations.
func synthFragment(seq uint32, baseTime uint64, durations []uint32) []byte {
	mfhd := fullBox("mfhd", 0, 0, u32b(seq))
	tfhd := fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, u32b(1))
	tfdt := fullBox("tfdt", 0, 0, u32b(uint32(baseTime)))
	var trunBody []byte
	trunBody = append(trunBody, u32b(uint32(len(durations)))...)
	trunBody = append(trunBody, u32b(0)...) // data_offset placeholder
	for _, d := range durations {
		trunBody = append(trunBody, u32b(d)...)
		trunBody = append(trunBody, u32b(10)...) // size
	}
	trun := fullBox("trun", 0, trunDataOffset|trunSampleDuration|trunSampleSize, trunBody)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	moof := box("moof", append(mfhd, traf...))
	payload := make([]byte, 10*len(durations))
	mdat := box("mdat", payload)
	return append(moof, mdat...)
}

func TestParseInit(t *testing.T) {
	info, err := ParseInit(synthInit(t, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if info.Timescale != 1000 {
		t.Fatalf("timescale = %d", info.Timescale)
	}
	if _, err := ParseInit(box("ftyp", nil)); err == nil {
		t.Fatal("init without moov must fail")
	}
}

func TestParseFragmentOK(t *testing.T) {
	data := synthFragment(7, 3500, []uint32{25, 25, 25, 25})
	f, err := ParseFragment(data, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != 7 || f.BaseTime != 3500 || len(f.Samples) != 4 {
		t.Fatalf("unexpected fragment: %+v", f)
	}
	if f.Duration != 100 || f.PayloadSize != 40 {
		t.Fatalf("totals wrong: dur=%d size=%d", f.Duration, f.PayloadSize)
	}
}

func TestParseFragmentRejectsZeroDuration(t *testing.T) {
	data := synthFragment(1, 0, []uint32{25, 0, 25})
	_, err := ParseFragment(data, 1000)
	if !errors.Is(err, ErrZeroDuration) {
		t.Fatalf("want ErrZeroDuration, got %v", err)
	}
}

func TestParseFragmentRejectsTruncatedTRUN(t *testing.T) {
	// Declares 5 samples but the table only holds two entries.
	mfhd := fullBox("mfhd", 0, 0, u32b(1))
	tfhd := fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, u32b(1))
	tfdt := fullBox("tfdt", 0, 0, u32b(0))
	body := append(u32b(5), u32b(0)...)
	body = append(body, u32b(25)...)
	body = append(body, u32b(10)...)
	trun := fullBox("trun", 0, trunDataOffset|trunSampleDuration|trunSampleSize, body)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", make([]byte, 20))
	_, err := ParseFragment(append(moof, mdat...), 1000)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func TestParseFragmentRejectsShortMdat(t *testing.T) {
	// Samples claim 10 bytes each but mdat only holds 5.
	data := synthFragment(1, 0, []uint32{25, 25})
	boxes, _ := ScanBoxes(data)
	shortMdat := box("mdat", make([]byte, 5))
	moof := data[:boxes[1].Start]
	_, err := ParseFragment(append(append([]byte{}, moof...), shortMdat...), 1000)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated for short mdat, got %v", err)
	}
}

func TestParseFragmentMissingBoxes(t *testing.T) {
	if _, err := ParseFragment(box("mdat", []byte("x")), 1000); err == nil {
		t.Fatal("fragment without moof must fail")
	}
}
