package meta

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestSubtitleCueRoundTrip(t *testing.T) {
	c := Cue{Seq: 42, StartMS: 1500, EndMS: 4200, Text: "edge node alive"}
	rec, err := NewSubtitleRecord(3, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCue(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatalf("cue roundtrip %+v != %+v", got, c)
	}
	back, err := ParseCanonical(rec.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != KindSubtitle || back.TrackID != 3 || back.TimeTicks != 1500 || back.DurationTick != 2700 {
		t.Fatalf("record roundtrip %+v", back)
	}
	if !bytes.Equal(back.Payload, rec.Payload) || back.Digest != rec.Digest {
		t.Fatal("payload or digest changed in roundtrip")
	}
	if err := back.CheckDigest(); err != nil {
		t.Fatal(err)
	}
}

func TestSubtitleRejectsBadCue(t *testing.T) {
	for _, c := range []Cue{
		{Seq: 1, StartMS: 100, EndMS: 100, Text: "x"},
		{Seq: 1, StartMS: 200, EndMS: 100, Text: "x"},
		{Seq: 1, StartMS: 0, EndMS: 10, Text: ""},
		{Seq: 1, StartMS: 0, EndMS: 10, Text: "two\nlines"},
	} {
		if _, err := NewSubtitleRecord(1, c); !errors.Is(err, ErrMetaFormat) {
			t.Fatalf("cue %+v accepted", c)
		}
	}
	if _, err := ParseCue([]byte("NOTVTT")); !errors.Is(err, ErrMetaFormat) {
		t.Fatalf("bad header: %v", err)
	}
}

func TestChapterRoundTrip(t *testing.T) {
	rec, err := NewChapterRecord(2, 4, 60000, 90000, "Segment B")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := ParseChapter(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Index != 4 || ch.StartMS != 60000 || ch.EndMS != 90000 || ch.Title != "Segment B" {
		t.Fatalf("chapter %+v", ch)
	}
	if _, err := ParseChapter([]byte("1|2|x|t")); !errors.Is(err, ErrMetaFormat) {
		t.Fatalf("bad chapter accepted")
	}
}

func TestCanonicalRejectsTampering(t *testing.T) {
	rec, err := NewSubtitleRecord(1, Cue{Seq: 1, StartMS: 0, EndMS: 500, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	enc := rec.Canonical()
	// flip a payload byte: digest must fail
	tampered := append([]byte(nil), enc...)
	tampered[len(tampered)-33] ^= 0x01
	if _, err := ParseCanonical(tampered); !errors.Is(err, ErrMetaDigest) {
		t.Fatalf("tampered payload accepted: %v", err)
	}
	// truncation
	if _, err := ParseCanonical(enc[:len(enc)-10]); !errors.Is(err, ErrMetaFormat) {
		t.Fatalf("truncated record accepted: %v", err)
	}
	// trailing bytes
	if _, err := ParseCanonical(append(enc, 0)); !errors.Is(err, ErrMetaFormat) {
		t.Fatalf("trailing bytes accepted: %v", err)
	}
}

func TestID3RoundTrip(t *testing.T) {
	tag := ID3Tag{Frames: []ID3Frame{
		{ID: ID3FrameTIT2, Encoding: 3, Text: "edge broadcast"},
		{ID: ID3FrameTDRC, Encoding: 3, Text: "2026-11-23T10:04:05.500Z"},
		{ID: ID3FrameTXXX, Encoding: 3, Owner: "mediapipe", Text: "node=7"},
		{ID: ID3FramePRIV, Owner: "mediapipe", Private: []byte{0xde, 0xad, 0xbe, 0xef}},
	}}
	b, err := EncodeID3(tag)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseID3(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != 4 {
		t.Fatalf("frames %d", len(got.Frames))
	}
	if got.Frames[0].Text != "edge broadcast" || got.Frames[2].Owner != "mediapipe" {
		t.Fatalf("frames %+v", got.Frames)
	}
	if got.Frames[1].TimestampTicks == nil || *got.Frames[1].TimestampTicks != 1795428245500 {
		t.Fatalf("TDRC ticks %v", got.Frames[1].TimestampTicks)
	}
	if !bytes.Equal(got.Frames[3].Private, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("priv %x", got.Frames[3].Private)
	}
}

func TestID3RejectsUnsupportedAndTruncated(t *testing.T) {
	good, err := EncodeID3(ID3Tag{Frames: []ID3Frame{{ID: ID3FrameTIT2, Encoding: 3, Text: "t"}}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"short":           good[:5],
		"magic":           append([]byte("XD3"), good[3:]...),
		"version":         func() []byte { c := append([]byte(nil), good...); c[3] = 3; return c }(),
		"flags":           func() []byte { c := append([]byte(nil), good...); c[5] |= 0x80; return c }(),
		"size":            func() []byte { c := append([]byte(nil), good...); c[9] = 0x7f; return c }(),
		"truncated frame": func() []byte { c := append([]byte(nil), good...); return c[:len(c)-1] }(),
	}
	for name, b := range cases {
		if _, err := ParseID3(b); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := EncodeID3(ID3Tag{Frames: []ID3Frame{{ID: "APIC"}}}); !errors.Is(err, ErrUnsupportedID3) {
		t.Fatalf("unsupported frame encoded: %v", err)
	}
	if _, err := EncodeID3(ID3Tag{Frames: []ID3Frame{{ID: ID3FrameTIT2, Encoding: 3}, {ID: ID3FrameTIT2, Encoding: 3}}}); !errors.Is(err, ErrMetaFormat) {
		t.Fatalf("duplicate frame encoded: %v", err)
	}
}

func TestEmsgRoundTrip(t *testing.T) {
	e := Emsg{SchemeIDURI: "urn:mediapipe:chapter", Value: "seg-2", Timescale: 1000,
		Presentation: 42000, EventDuration: 500, ID: 9, Message: []byte("chapter payload")}
	b, err := EncodeEmsg(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseEmsgPayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemeIDURI != e.SchemeIDURI || got.Presentation != 42000 || string(got.Message) != string(e.Message) {
		t.Fatalf("emsg %+v", got)
	}
	rec, err := NewEmsgRecord(5, e)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Label != "chapter" || rec.TimeTicks != 42000 || rec.Timescale != 1000 {
		t.Fatalf("record %+v", rec)
	}
}

func TestEmsgRejectsMalformed(t *testing.T) {
	good, _ := EncodeEmsg(Emsg{SchemeIDURI: "urn:x", Timescale: 1000, Presentation: 1})
	cases := map[string][]byte{
		"short":   good[:3],
		"version": func() []byte { c := append([]byte(nil), good...); c[0] = 0; return c }(),
		"unterminated": func() []byte {
			c := append([]byte(nil), good...)
			for i := range c {
				if c[i] == 0 {
					c[i] = 1
				}
			}
			return c
		}(),
		"body": good[:len(good)-1],
	}
	for name, b := range cases {
		if _, err := ParseEmsgPayload(b); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSEIRoundTripAndEscaping(t *testing.T) {
	s := SEI{UUID: SEIUUID, Payload: []byte{0, 0, 1, 0, 0, 2, 0xff, 0x00, 0x00, 0x03}}
	nal, err := EncodeSEI(s)
	if err != nil {
		t.Fatal(err)
	}
	if nal[0] != 0x06 {
		t.Fatalf("nal type %#x", nal[0])
	}
	got, err := ParseSEI(nal)
	if err != nil {
		t.Fatal(err)
	}
	if got.UUID != SEIUUID || !bytes.Equal(got.Payload, s.Payload) {
		t.Fatalf("sei %+v", got)
	}
	rec, err := NewSEIRecord(1, 250, s)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != KindSEI || rec.TimeTicks != 250 {
		t.Fatalf("record %+v", rec)
	}
}

func TestSEIRejectsWrongUUIDAndNAL(t *testing.T) {
	bad := SEI{UUID: "XXXXXXXXXXXXXXXX", Payload: []byte{1}}
	nal, err := EncodeSEI(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSEI(nal); err == nil {
		t.Fatal("wrong uuid accepted")
	}
	if _, err := ParseSEI([]byte{0x05, 0, 0}); err == nil {
		t.Fatal("non-sei nal accepted")
	}
}

func TestPayloadDigestHex(t *testing.T) {
	d := PayloadDigest([]byte("abc"))
	if len(d) != 64 {
		t.Fatalf("digest length %d", len(d))
	}
	if _, err := hex.DecodeString(d); err != nil {
		t.Fatalf("not hex: %v", err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if d != want {
		t.Fatalf("digest = %s", d)
	}
}
