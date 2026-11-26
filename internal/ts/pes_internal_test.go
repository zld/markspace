package ts

import "testing"

func TestEncodeDecodeTimestampRoundtrip(t *testing.T) {
	cases := []uint64{0, 1, 90000, 8589898592, 8589934592, 8589952592, 1<<33 - 1}
	for _, ts := range cases {
		for _, marker := range []byte{0x2, 0x3, 0x1} {
			b := EncodeTS(marker, ts)
			got := decodeTS(b[:])
			// decodeTS yields the 33-bit field; values above 2^33 encode mod.
			want := ts & (1<<33 - 1)
			if got != want {
				t.Fatalf("marker=%x ts=%d decoded=%d want %d", marker, ts, got, want)
			}
			if b[0]>>4 != marker {
				t.Fatalf("marker nibble wrong: %02x", b[0])
			}
		}
	}
}
