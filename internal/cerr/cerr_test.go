package cerr

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	e := New(CodeTruncatedData, 1905, "/Segment/Cluster/SimpleBlock", "element data truncated")
	want := "EBML_TRUNCATED_DATA offset=1905 path=/Segment/Cluster/SimpleBlock: element data truncated"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestIsThroughWrapping(t *testing.T) {
	e := New(CodeTimestampOverflow, -1, "clock", "overflow")
	wrapped := fmt.Errorf("matroska: %w", e)
	if !Is(wrapped, CodeTimestampOverflow) {
		t.Fatal("Is did not match wrapped error")
	}
	if Is(wrapped, CodeTruncatedData) {
		t.Fatal("Is matched wrong code")
	}
	if Is(errors.New("plain"), CodeTruncatedData) {
		t.Fatal("Is matched non-cerr error")
	}
}

func TestLocate(t *testing.T) {
	code, off, path := Locate(New(CodeSizeOverflow, 42, "/Segment", "boom"))
	if code != CodeSizeOverflow || off != 42 || path != "/Segment" {
		t.Fatalf("got %s %d %s", code, off, path)
	}
	code, off, _ = Locate(errors.New("plain"))
	if code != "INTERNAL" || off != -1 {
		t.Fatalf("got %s %d", code, off)
	}
}
