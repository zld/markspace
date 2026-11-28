package codec

import (
	"testing"

	"mediacheck/internal/fault"
)

func annexB(units ...[]byte) []byte {
	var out []byte
	for _, u := range units {
		out = append(out, 0, 0, 0, 1)
		out = append(out, u...)
	}
	return out
}

func TestParseH264(t *testing.T) {
	data := annexB(
		[]byte{0x67, 1, 2, 3}, // SPS
		[]byte{0x68, 4, 5},    // PPS
		[]byte{0x65, 9, 9, 9}, // IDR
		[]byte{0x41, 7},       // non-IDR slice
	)
	units, err := Units(data, H264)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 4 {
		t.Fatalf("got %d units", len(units))
	}
	if !units[2].Keyframe || units[3].Keyframe {
		t.Fatal("keyframe flags wrong")
	}
	if units[1].Offset != 12 || units[1].Name != "PPS" {
		t.Fatalf("unit1 %+v", units[1])
	}
}

func TestParseH265(t *testing.T) {
	data := annexB(
		[]byte{0x40, 0x01, 1}, // VPS
		[]byte{0x26, 0x01, 2}, // IDR
		[]byte{0x02, 0x01, 3}, // TRAIL
	)
	units, err := Units(data, H265)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 3 || !units[1].Keyframe || units[2].Keyframe {
		t.Fatalf("units %+v", units)
	}
}

func TestParseAV1(t *testing.T) {
	data := []byte{
		0x0A, 2, 1, 2, // seq_header, size 2
		0x32, 3, 0x00, 9, 9, // frame, frame_type KEY
		0x32, 3, 0xC0, 8, 8, // frame, frame_type INTER
	}
	units, err := Units(data, AV1)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 3 || !units[1].Keyframe || units[2].Keyframe {
		t.Fatalf("units %+v", units)
	}
	if units[1].Offset != 4 {
		t.Fatalf("frame obu offset %d", units[1].Offset)
	}
}

func TestBadHeaderOffsets(t *testing.T) {
	// corrupt the header byte of the second unit in each codec
	// unit layout: SC(0-3) hdr(4) payload(5-9) SC(10-13) hdr(14)
	h264 := annexB([]byte{0x67, 1, 2, 3, 4, 5}, []byte{0x65, 9})
	h264[14] |= 0x80
	assertOffset(t, err2(Units(h264, H264)), 14)

	h265 := annexB([]byte{0x40, 0x01, 1, 2, 3, 4}, []byte{0x26, 0x01, 9})
	h265[14] |= 0x80
	assertOffset(t, err2(Units(h265, H265)), 14)

	av1 := []byte{0x0A, 2, 1, 2, 0x32, 1, 0x00}
	av1[4] |= 0x80
	assertOffset(t, err2(Units(av1, AV1)), 4)
}

func TestTruncatedUnits(t *testing.T) {
	// start code with no header byte at all
	tr := annexB([]byte{0x67, 1, 2})
	tr = append(tr, 0, 0, 0, 1)
	err := err2(Units(tr, H264))
	if !fault.HasCode(err, fault.ErrTruncatedUnit) {
		t.Fatalf("h264: %v", err)
	}
	assertOffset(t, err, int64(len(tr)))
	// h265 header needs 2 bytes, only 1 present
	tr2 := annexB([]byte{0x40, 0x01, 1})
	tr2 = append(tr2, 0, 0, 0, 1, 0x26)
	assertOffset(t, err2(Units(tr2, H265)), int64(len(tr2)))
	// av1 obu declares 10 payload bytes, 2 remain
	av1 := []byte{0x32, 10, 1, 2}
	assertOffset(t, err2(Units(av1, AV1)), 4)
}

func TestEmptyStream(t *testing.T) {
	for _, k := range []Kind{H264, H265, AV1} {
		units, err := Units(nil, k)
		if err != nil || len(units) != 0 {
			t.Fatalf("kind %v: %v %v", k, units, err)
		}
	}
}

func err2(_ []Unit, err error) error { return err }

func assertOffset(t *testing.T, err error, want int64) {
	t.Helper()
	fe, ok := err.(*fault.Error)
	if !ok {
		t.Fatalf("not a fault.Error: %v", err)
	}
	if fe.Offset != want {
		t.Fatalf("offset %d want %d (%v)", fe.Offset, want, fe)
	}
}
