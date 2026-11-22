package ingest

import (
	"strings"
	"testing"

	"mediad/internal/fixture"
)

// TestMP4CorruptionRobust checks that truncation at any point and byte flips
// in the box headers never panic and never return an error: damage is always
// reported as findings, never as a crash or an out-of-bounds read.
func TestMP4CorruptionRobust(t *testing.T) {
	st, err := fixture.Generate(fixture.DemoDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range st.Files {
		if f.Kind != fixture.KindMP4 {
			continue
		}
		for cut := 0; cut < len(f.Data); cut += 37 {
			a := &Asset{Name: f.Name, Format: "mp4", Data: f.Data[:cut]}
			if _, err := ParseMP4(a); err != nil {
				t.Fatalf("cut at %d: %v", cut, err)
			}
		}
		limit := 3000
		if limit > len(f.Data) {
			limit = len(f.Data)
		}
		for off := 0; off < limit; off += 13 {
			data := append([]byte(nil), f.Data...)
			data[off] ^= 0xff
			a := &Asset{Name: f.Name, Format: "mp4", Data: data}
			if _, err := ParseMP4(a); err != nil {
				t.Fatalf("flip at %d: %v", off, err)
			}
		}
	}
}

// TestSubtitleGarbageRobust feeds arbitrary bytes to both subtitle parsers.
func TestSubtitleGarbageRobust(t *testing.T) {
	inputs := []string{
		"\x00\x01\x02\xff\xfe",
		"1\n00:00:01,000 -->\n\n",
		"WEBVTT\n\n-->\n\n",
		strings.Repeat("0", 5000),
		"1\n00:00:01,000 --> 00:00:02,000\n",
	}
	for _, in := range inputs {
		a := &Asset{Name: "g.srt", Format: "srt", Data: []byte(in)}
		if _, err := ParseSRT(a); err != nil {
			t.Fatalf("srt %q: %v", in, err)
		}
		b := &Asset{Name: "g.vtt", Format: "vtt", Data: []byte(in)}
		if _, err := ParseVTT(b); err != nil {
			t.Fatalf("vtt %q: %v", in, err)
		}
	}
}

// TestRTPGarbageRobust exercises the length-prefixed reader with hostile
// framing: oversized, zero and truncated lengths.
func TestRTPGarbageRobust(t *testing.T) {
	cases := [][]byte{
		{0, 0, 0, 0},
		{0xff, 0xff, 0xff, 0xff},
		{0, 0, 0, 1},
		{0, 0, 0, 200, 0x80},
	}
	for _, data := range cases {
		a := &Asset{Name: "x.rtp", Format: "rtp", Data: data}
		st, err := ParseRTP(a, 90000)
		if err != nil {
			t.Fatalf("%x: %v", data, err)
		}
		if len(st.Records) != 0 {
			t.Fatalf("%x: parsed %d records from garbage", data, len(st.Records))
		}
	}
}
