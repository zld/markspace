package verify

import (
	"strings"
	"testing"
)

func TestSum256Stable(t *testing.T) {
	got, err := Sum256(strings.NewReader("media"))
	if err != nil {
		t.Fatal(err)
	}
	if got != Sum256Bytes([]byte("media")) {
		t.Fatal("stream and byte sums differ")
	}
	if len(got) != 64 {
		t.Fatalf("hex length %d", len(got))
	}
	if Sum256Bytes([]byte("a")) == Sum256Bytes([]byte("b")) {
		t.Fatal("distinct inputs must differ")
	}
}
