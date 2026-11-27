package archive

import "testing"

func TestDigestOfIsStable(t *testing.T) {
	a := DigestOf([]byte("encoded-av1-av1-bytes"))
	b := DigestOf([]byte("encoded-av1-av1-bytes"))
	if a != b || len(a) != 64 {
		t.Fatalf("digest = %q / %q", a, b)
	}
	if a == DigestOf([]byte("different")) {
		t.Fatal("different content must differ")
	}
	if DigestString(" ABCDEF ") != "abcdef" {
		t.Fatal("digest normalisation failed")
	}
}

func TestEntrySummary(t *testing.T) {
	e := Entry{Digest: "0123456789abcdef0123", Codec: "av1", Bytes: 10, PSNRdB: 41.5, Verified: true}
	if got := e.Summary(); got != "0123456789ab codec=av1 bytes=10 psnr=41.50 verified=true" {
		t.Fatalf("summary = %q", got)
	}
}
