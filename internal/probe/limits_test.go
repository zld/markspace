package probe

import (
	"bytes"
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

func TestShortInputsFailClearly(t *testing.T) {
	for _, in := range [][]byte{nil, {}, []byte("abc"), []byte{0, 0, 0, 8, 'f', 't', 'y', 'p'}} {
		if _, err := Sniff(in); err == nil {
			t.Fatalf("input %q should fail", in)
		}
	}
}

func TestOversizedKLVValueIsRejected(t *testing.T) {
	data := fixture.BuildMXF(fixture.DefaultMXFSpec())
	// Corrupt a length field to a huge BER value at the first KLV after run-in.
	idx := bytes.Index(data, []byte{0x06, 0x0e, 0x2b, 0x34})
	if idx < 0 {
		t.Fatal("no KLV found")
	}
	bad := append([]byte(nil), data...)
	bad[idx+16] = 0x84
	bad[idx+17] = 0x7f
	bad[idx+18] = 0xff
	bad[idx+19] = 0xff
	bad[idx+20] = 0xff
	_, err := ProbeMXF(bad)
	if err == nil {
		t.Fatal("oversized value must fail")
	}
	if k := clock.AsKind(err); k != clock.KindOutOfRange && k != clock.KindTruncated {
		t.Fatalf("kind = %q err=%v", k, err)
	}
}

func TestMXFGarbageAfterHeaderIsBounded(t *testing.T) {
	data := fixture.BuildMXF(fixture.DefaultMXFSpec())
	// Flip the partition KLV length so traversal lands on arbitrary bytes.
	idx := bytes.Index(data, []byte{0x06, 0x0e, 0x2b, 0x34})
	bad := append([]byte(nil), data...)
	bad[idx+16] = 0x00
	_, err := ProbeMXF(bad)
	if err == nil {
		t.Fatal("corrupt partition length must not parse silently")
	}
}
