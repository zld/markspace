package mp4

import (
	"encoding/binary"
	"errors"
	"testing"

	"mediad/internal/timeline"
)

func fullbox(ver uint8, flags uint32) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func i32(v int32) []byte { return u32(uint32(v)) }

// buildMoofMdat assures a two-track-independent single traf fragment with
// trun flags data-offset|sample-duration|sample-size.
func buildMoofMdat(t *testing.T, seq, baseT uint32, sizes, durations []uint32, mdatLen int) ([]byte, int64, int64) {
	t.Helper()
	mfhd := box("mfhd", append(fullbox(0, 0), u32(seq)...))
	tfhd := box("tfhd", append(fullbox(0, 0), u32(1)...))
	tfdt := box("tfdt", append(fullbox(0, 0), u32(baseT)...))
	trunFlags := uint32(0x000001 | 0x000100 | 0x000200)
	trunPay := append(fullbox(0, trunFlags), u32(uint32(len(sizes)))...)
	trunPay = append(trunPay, make([]byte, 4)...) // data-offset placeholder
	for i, s := range sizes {
		trunPay = append(trunPay, u32(durations[i])...)
		trunPay = append(trunPay, u32(s)...)
	}
	trun := box("trun", trunPay)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", make([]byte, mdatLen))
	data := append(moof, mdat...)
	moofOff, mdatPayload := int64(0), int64(len(moof)+8)
	// patch data offset (relative to moof)
	trunOff := len(moof) - len(trun)
	offPos := trunOff + 8 + 4 + 4
	binary.BigEndian.PutUint32(data[offPos:offPos+4], uint32(mdatPayload-moofOff))
	return data, int64(len(moof)), mdatPayload
}

func decodeFirst(t *testing.T, data []byte) *Fragment {
	t.Helper()
	r := NewSeeker(data)
	boxes, err := ScanTopLevel(r, "f.m4s", Limits{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	frag, err := DecodeFragment(r, "f.m4s", boxes[0], boxes[1], Limits{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return frag
}

func TestDecodeFragmentOK(t *testing.T) {
	sizes := []uint32{10, 20, 30}
	durs := []uint32{100, 100, 100}
	data, _, mdatPayload := buildMoofMdat(t, 7, 5000, sizes, durs, 60)
	frag := decodeFirst(t, data)
	if frag.SeqNum != 7 {
		t.Fatalf("seq = %d, want 7", frag.SeqNum)
	}
	if len(frag.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(frag.Tracks))
	}
	tf := frag.Tracks[0]
	if tf.TrackID != 1 || tf.BaseMediaT != 5000 {
		t.Fatalf("tfhd/tfdt mismatch: %+v", tf)
	}
	if len(tf.Samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(tf.Samples))
	}
	cursor := mdatPayload
	for i, s := range tf.Samples {
		if s.DataOffset != cursor || s.Size != int64(sizes[i]) || s.Duration != 100 {
			t.Fatalf("sample %d mismatch: %+v", i, s)
		}
		cursor += int64(sizes[i])
	}
	if frag.ContentRange[1] != int64(len(data)) {
		t.Fatalf("content range end = %d, want %d", frag.ContentRange[1], len(data))
	}
}

func TestDecodeSampleEscapesMdat(t *testing.T) {
	// mdat only holds 25 bytes but samples need 60.
	data, _, _ := buildMoofMdat(t, 1, 0, []uint32{10, 20, 30}, []uint32{1, 1, 1}, 25)
	r := NewSeeker(data)
	boxes, err := ScanTopLevel(r, "cut.m4s", Limits{})
	if err != nil {
		// mdat header itself is intact; scan succeeds, decode must fail.
		t.Fatalf("scan: %v", err)
	}
	_, err = DecodeFragment(r, "cut.m4s", boxes[0], boxes[1], Limits{})
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrBoxTruncated {
		t.Fatalf("want E_BOX_TRUNCATED, got %v", err)
	}
	// First escaping sample begins at mdat payload + 10.
	if me.Offset != int64(len(data)-25)+10 {
		t.Fatalf("escape offset = %d, want %d", me.Offset, int64(len(data)-25)+10)
	}
}

func TestDecodeMissingMfhd(t *testing.T) {
	tfhd := box("tfhd", append(fullbox(0, 0), u32(1)...))
	moof := box("moof", box("traf", tfhd))
	r := NewSeeker(moof)
	boxes, err := ScanTopLevel(r, "no-mfhd.m4s", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeFragment(r, "no-mfhd.m4s", boxes[0], nil, Limits{})
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrMissingField {
		t.Fatalf("want E_MISSING_FIELD, got %v", err)
	}
}

func TestDecodeNoSampleSizeSource(t *testing.T) {
	mfhd := box("mfhd", append(fullbox(0, 0), u32(1)...))
	// trun with duration+data-offset flags but NO sample-size flag; tfhd no default.
	tfhd := box("tfhd", append(fullbox(0, 0), u32(1)...))
	trunPay := append(fullbox(0, 0x000101), u32(1)...)
	trunPay = append(trunPay, make([]byte, 4)...)
	trunPay = append(trunPay, u32(100)...)
	traf := box("traf", append(tfhd, box("trun", trunPay)...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", make([]byte, 16))
	r := NewSeeker(append(moof, mdat...))
	boxes, err := ScanTopLevel(r, "nosize.m4s", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeFragment(r, "nosize.m4s", boxes[0], boxes[1], Limits{})
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrMissingField {
		t.Fatalf("want E_MISSING_FIELD for size source, got %v", err)
	}
}

func TestDecodeSampleCountLimit(t *testing.T) {
	data, _, _ := buildMoofMdat(t, 1, 0, []uint32{1, 1, 1}, []uint32{1, 1, 1}, 3)
	r := NewSeeker(data)
	boxes, _ := ScanTopLevel(r, "lim.m4s", Limits{})
	_, err := DecodeFragment(r, "lim.m4s", boxes[0], boxes[1], Limits{MaxSamples: 2})
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrLimitExceeded {
		t.Fatalf("want E_LIMIT, got %v", err)
	}
}
