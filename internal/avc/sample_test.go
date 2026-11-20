package avc

import (
	"encoding/binary"
	"errors"
	"testing"
)

func nal(typ byte, rbsp ...byte) []byte {
	out := []byte{0x60 | typ}
	return append(out, rbsp...)
}

func sample(lengthSize int, nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		var lp [4]byte
		binary.BigEndian.PutUint32(lp[:], uint32(len(n)))
		out = append(out, lp[4-lengthSize:]...)
		out = append(out, n...)
	}
	return out
}

func TestParseAvcC(t *testing.T) {
	n, err := ParseAvcC([]byte{1, 100, 0, 40, 0xFF, 0xE1, 0})
	if err != nil || n != 4 {
		t.Fatalf("got %d, %v", n, err)
	}
	if _, err := ParseAvcC([]byte{1, 100, 0}); err == nil {
		t.Fatal("short avcC accepted")
	}
	if _, err := ParseAvcC([]byte{2, 100, 0, 40, 0xFF, 0xE1, 0}); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestClassifyTypes(t *testing.T) {
	cases := []struct {
		name string
		nal  []byte
		want FrameType
	}{
		{"idr", nal(5, 0x88, 0x99), IDR},
		{"i-slice", nal(1, 0xB0), I},  // ue(0) ue(2)
		{"p-slice", nal(1, 0xC0), P},  // ue(0) ue(0)
		{"b-slice", nal(1, 0xA0), B},  // ue(0) ue(1)
		{"si-slice", nal(1, 0x94), I}, // ue(0) ue(4): bits 1 00101
		{"no-vcl", nal(6, 0x00), Unknown},
	}
	for _, tc := range cases {
		c, err := Classify(sample(4, tc.nal), 4)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c.Type != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, c.Type, tc.want)
		}
	}
}

func TestClassifyMultiSlice(t *testing.T) {
	s := sample(4, nal(1, 0xC0), nal(1, 0xC0), nal(7, 0x01))
	c, err := Classify(s, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MultiSlice || c.Slices != 2 || c.Type != P {
		t.Fatalf("got %+v", c)
	}
}

func TestClassifyIDRWins(t *testing.T) {
	s := sample(4, nal(1, 0xC0), nal(5, 0x88))
	c, err := Classify(s, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != IDR {
		t.Fatalf("got %s", c.Type)
	}
}

func TestClassifyTruncated(t *testing.T) {
	if _, err := Classify([]byte{0, 0, 0, 10, 0x65}, 4); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	if _, err := Classify([]byte{0, 0}, 4); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestEmulationPrevention(t *testing.T) {
	// RBSP 00 00 03 00 ... should decode like 00 00 00 ...
	got := stripEPB([]byte{0x11, 0x00, 0x00, 0x03, 0x22, 0x00, 0x00, 0x03, 0x33})
	want := []byte{0x11, 0x00, 0x00, 0x22, 0x00, 0x00, 0x33}
	if string(got) != string(want) {
		t.Fatalf("got %x want %x", got, want)
	}
}
