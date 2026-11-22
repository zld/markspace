package rtp

import (
	"errors"
	"testing"
)

func TestPayloadRoundTrip(t *testing.T) {
	p := Payload{Flags: FlagKeyframe | FlagFragment, TrackID: 1, Frame: 7, MediaTicks: 90000, Data: []byte("edge-fragment-01")}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackID != p.TrackID || got.Frame != p.Frame || got.MediaTicks != p.MediaTicks || got.Flags != p.Flags {
		t.Fatalf("roundtrip mismatch %+v", got)
	}
	if string(got.Data) != string(p.Data) {
		t.Fatalf("data mismatch %q", got.Data)
	}
	if got.Digest() != p.Digest() {
		t.Fatal("digest mismatch")
	}
}

func TestPayloadRejectsCorruptionAndTruncation(t *testing.T) {
	p := Payload{TrackID: 2, Frame: 1, MediaTicks: 10, Data: []byte("payload")}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"short", b[:10]},
		{"magic", append([]byte{'X'}, b[1:]...)},
		{"version", func() []byte { c := append([]byte(nil), b...); c[4] = 9; return c }()},
		{"length", func() []byte { c := append([]byte(nil), b...); c[21]++; return c }()},
		{"checksum", func() []byte { c := append([]byte(nil), b...); c[len(c)-1] ^= 0xff; return c }()},
		{"data corruption", func() []byte { c := append([]byte(nil), b...); c[22] ^= 0xff; return c }()},
		{"track zero", func() []byte { c := append([]byte(nil), b...); c[6], c[7] = 0, 0; return c }()},
	} {
		if _, err := ParsePayload(tc.data); err == nil {
			t.Fatalf("%s: corrupted payload accepted", tc.name)
		}
	}
}

func TestPayloadSizeLimit(t *testing.T) {
	p := Payload{TrackID: 1, Data: make([]byte, MaxPayloadData+1)}
	if _, err := p.Marshal(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized payload accepted: %v", err)
	}
}
