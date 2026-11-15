package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func samplePacket(n uint32) Packet {
	s := make([]int16, SamplesPerFrame)
	for i := range s {
		s[i] = int16((n*7 + uint32(i)*13) % 4096)
	}
	return Packet{
		Version: Version,
		Seq:     uint16(n),
		FrameNo: n,
		SendNS:  uint64(n) * 20_000_000,
		Samples: s,
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	p := samplePacket(5)
	buf := make([]byte, MaxPacketSize)
	n := Encode(buf, p)
	if n != MaxPacketSize {
		t.Fatalf("encoded size = %d, want %d", n, MaxPacketSize)
	}
	got, err := Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Seq != p.Seq || got.FrameNo != p.FrameNo || got.SendNS != p.SendNS {
		t.Fatalf("header mismatch: %+v vs %+v", got, p)
	}
	if !equalSamples(got.Samples, p.Samples) {
		t.Fatal("payload samples mismatch")
	}
}

func TestDecodeErrors(t *testing.T) {
	p := samplePacket(1)
	buf := make([]byte, MaxPacketSize)
	n := Encode(buf, p)

	cases := []struct {
		name string
		mut  func([]byte) []byte
		want error
	}{
		{"truncated header", func(b []byte) []byte { return b[:4] }, ErrShort},
		{"unknown version", func(b []byte) []byte {
			c := clone(b)
			c[0] = 99
			return c
		}, ErrUnknownVersion},
		{"illegal length", func(b []byte) []byte {
			c := clone(b)
			c[17] = 8 // length becomes 2056 instead of 640
			return c
		}, ErrIllegalLength},
		{"short datagram", func(b []byte) []byte { return clone(b)[:n-4] }, ErrIllegalLength},
		{"corrupted checksum", func(b []byte) []byte {
			c := clone(b)
			c[30] ^= 0xFF
			return c
		}, ErrChecksum},
		{"corrupted payload", func(b []byte) []byte {
			c := clone(b)
			c[HeaderSize+10] ^= 0x01 // checksum field unchanged -> mismatch
			return c
		}, ErrChecksum},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.mut(buf[:n])); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSeqWraparoundCompare(t *testing.T) {
	cases := []struct {
		a, b  uint16
		wantD int
		amb   bool
	}{
		{5, 4, 1, false},
		{4, 5, -1, false},
		{0, 65535, 1, false}, // wrap forward
		{65535, 0, -1, false},
		{32767, 65534, -32767, false},
		{32768, 0, 0, true},     // exact half-interval: ambiguous
		{32767, 65535, 0, true}, // same, via negative numbers
		{10, 10, 0, false},
	}
	for _, c := range cases {
		d, amb := SeqCompare(c.a, c.b)
		if d != c.wantD || amb != c.amb {
			t.Errorf("SeqCompare(%d,%d)=(%d,%v), want (%d,%v)",
				c.a, c.b, d, amb, c.wantD, c.amb)
		}
		if amb {
			if err := checkAmbiguous(c.a, c.b); err != ErrAmbiguousSeq {
				t.Errorf("ambiguous pair %d,%d not rejected", c.a, c.b)
			}
		}
	}
}

// checkAmbiguous mirrors the rejection helper used by receive paths.
func checkAmbiguous(a, b uint16) error {
	if _, amb := SeqCompare(a, b); amb {
		return ErrAmbiguousSeq
	}
	return nil
}

func equalSamples(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func clone(b []byte) []byte { return bytes.Clone(b) }
