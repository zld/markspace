package fixture

import "testing"

func TestBERLengthRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 256, 65535, 1 << 20} {
		enc := BERLength(n)
		got, used, err := ParseBERLength(enc)
		if err != nil {
			t.Fatalf("len %d: %v", n, err)
		}
		if got != n || used != len(enc) {
			t.Fatalf("len %d round trip got %d used %d/%d", n, got, used, len(enc))
		}
	}
}

func TestParseBERLengthRejectsMalformed(t *testing.T) {
	if _, _, err := ParseBERLength(nil); err == nil {
		t.Fatal("empty BER must fail")
	}
	if _, _, err := ParseBERLength([]byte{0x80}); err == nil {
		t.Fatal("indefinite BER must fail")
	}
	if _, _, err := ParseBERLength([]byte{0x88, 1}); err == nil {
		t.Fatal("overlong BER must fail")
	}
}

func TestBuildMXFDeterministic(t *testing.T) {
	a := BuildMXF(DefaultMXFSpec())
	b := BuildMXF(DefaultMXFSpec())
	if len(a) != len(b) {
		t.Fatalf("lengths %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
}
