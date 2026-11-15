package wire

import (
	"errors"
	"testing"
)

func samplePacket() *Packet {
	return &Packet{
		StreamID:  7,
		Seq:       65535, // exercise wrap boundary
		Timestamp: 65535 * 20 % (1 << 32),
		Samples:   []int16{0, 100, -100, 32767, -32768},
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	pkt := samplePacket()
	pkt.Timestamp = 65534 * 20 // aligned to 20ms frame clock
	got, err := Decode(Encode(pkt), 7, 20)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.StreamID != pkt.StreamID || got.Seq != pkt.Seq || got.Timestamp != pkt.Timestamp {
		t.Fatalf("header mismatch: %+v", got)
	}
	if len(got.Samples) != len(pkt.Samples) {
		t.Fatalf("sample count %d != %d", len(got.Samples), len(pkt.Samples))
	}
	for i := range got.Samples {
		if got.Samples[i] != pkt.Samples[i] {
			t.Fatalf("sample %d: %d != %d", i, got.Samples[i], pkt.Samples[i])
		}
	}
}

func TestDecodeClassifiesErrors(t *testing.T) {
	good := Encode(samplePacket())
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrTooShort},
		{"short", good[:HeaderLen-1], ErrTooShort},
		{"no_payload", good[:HeaderLen], ErrBadPayload},
		{"odd_payload", append(good, 0x01), ErrBadPayload},
	}
	for _, tc := range cases {
		if _, err := Decode(tc.data, 7, 20); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}

	badVersion := append([]byte(nil), good...)
	badVersion[0] = 9
	if _, err := Decode(badVersion, 7, 20); !errors.Is(err, ErrBadVersion) {
		t.Errorf("version: %v", err)
	}

	badStream := append([]byte(nil), good...)
	badStream[5] ^= 0xff
	if _, err := Decode(badStream, 7, 20); !errors.Is(err, ErrBadStream) {
		t.Errorf("stream: %v", err)
	}

	badTS := Encode(&Packet{StreamID: 7, Seq: 1, Timestamp: 21, Samples: []int16{1}})
	if _, err := Decode(badTS, 7, 20); !errors.Is(err, ErrBadTimestamp) {
		t.Errorf("timestamp: %v", err)
	}
}

func TestCategoryLabels(t *testing.T) {
	for err, want := range map[error]string{
		ErrTooShort: "too_short", ErrBadVersion: "bad_version",
		ErrBadStream: "bad_stream", ErrBadTimestamp: "bad_timestamp",
		ErrBadPayload: "bad_payload",
	} {
		if got := Category(err); got != want {
			t.Errorf("Category(%v) = %q want %q", err, got, want)
		}
	}
}
