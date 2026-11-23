package stream

import (
	"testing"

	"mediqc/internal/fault"
)

func TestRTPRoundtrip(t *testing.T) {
	c := NewRTPCodec(0x15590)
	for i, payload := range [][]byte{[]byte("seg-0"), []byte("seg-1"), make([]byte, 1200)} {
		raw, err := c.Encode(payload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Decode(raw)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if len(got) != len(payload) {
			t.Fatalf("packet %d: payload len %d, want %d", i, len(got), len(payload))
		}
		for j := range got {
			if got[j] != payload[j] {
				t.Fatalf("packet %d byte %d differs", i, j)
			}
		}
	}
}

func TestRTPRejectsGarbage(t *testing.T) {
	c := NewRTPCodec(0x15590)
	if _, err := c.Decode([]byte{0x00, 0x01}); !fault.IsCode(err, fault.CodeTruncatedInput) {
		t.Fatalf("want E_TRUNCATED_INPUT, got %v", err)
	}
}

func TestRTPRejectsForeignSSRC(t *testing.T) {
	a := NewRTPCodec(0xAAAA)
	b := NewRTPCodec(0xBBBB)
	raw, err := a.Encode([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Decode(raw); !fault.IsCode(err, fault.CodeCacheTamper) {
		t.Fatalf("want E_CACHE_TAMPER, got %v", err)
	}
}
