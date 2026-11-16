package wire

import (
	"errors"
	"testing"
)

func samplePacket() Packet {
	pcm := make([]int16, SamplesPerFrame)
	for i := range pcm {
		pcm[i] = int16(i - SamplesPerFrame/2)
	}
	return Packet{
		Seq:       7,
		Timestamp: 7 * SamplesPerFrame,
		Samples:   SamplesPerFrame,
		Payload:   PCMToBytes(pcm),
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := samplePacket()
	b, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(b) != PacketSize {
		t.Fatalf("encoded size = %d, want %d", len(b), PacketSize)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Seq != want.Seq || got.Timestamp != want.Timestamp || got.Samples != want.Samples {
		t.Fatalf("header mismatch: got %+v want %+v", got, want)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Fatal("payload mismatch")
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	good, err := Encode(samplePacket())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrTooShort},
		{"short header", good[:10], ErrTooShort},
		{"truncated payload", good[:PacketSize-100], ErrBadLength},
		{"oversized", append(append([]byte{}, good...), 0), ErrBadLength},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.b); !errors.Is(err, tc.want) {
				t.Fatalf("Decode error = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("bad magic", func(t *testing.T) {
		b := append([]byte{}, good...)
		b[0] ^= 0xFF
		// fix checksum so only magic is wrong
		if _, err := Decode(b); !errors.Is(err, ErrBadMagic) {
			t.Fatalf("Decode error = %v, want %v", err, ErrBadMagic)
		}
	})
	t.Run("bad checksum", func(t *testing.T) {
		b := append([]byte{}, good...)
		b[20] ^= 0xFF // flip a payload bit
		if _, err := Decode(b); !errors.Is(err, ErrBadChecksum) {
			t.Fatalf("Decode error = %v, want %v", err, ErrBadChecksum)
		}
	})
	t.Run("bad sample count", func(t *testing.T) {
		p := samplePacket()
		p.Samples = 123
		b, err := Encode(p)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if _, err := Decode(b); !errors.Is(err, ErrBadSamples) {
			t.Fatalf("Decode error = %v, want %v", err, ErrBadSamples)
		}
	})
}

func TestPCMConversionRoundTrip(t *testing.T) {
	pcm := []int16{-32768, -1, 0, 1, 32767}
	got := BytesToPCM(PCMToBytes(pcm))
	for i := range pcm {
		if got[i] != pcm[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], pcm[i])
		}
	}
}
