package fault

import (
	"errors"
	"testing"
)

func TestNewAndContext(t *testing.T) {
	e := Newf(KindCorrupt, "stsz.entry_size[2]", 4096, "size %d exceeds mdat", 99).
		WithTrack(1).WithSeq(4242)
	if e.TrackIndex != 1 || !e.HasSeq || e.Seq != 4242 || e.Offset != 4096 {
		t.Fatalf("context not attached: %+v", e)
	}
	s := e.Error()
	for _, want := range []string{"corrupt", "stsz.entry_size[2]", "track=1", "seq=4242", "offset=4096"} {
		if !contains(s, want) {
			t.Fatalf("error %q missing %q", s, want)
		}
	}
}

func TestZeroOffsetIsValid(t *testing.T) {
	e := New(KindCorrupt, "ftyp", 0, "bad brand")
	if e.Offset != 0 {
		t.Fatalf("offset 0 must be preserved, got %d", e.Offset)
	}
}

func TestAsErrorAndIsCorrupt(t *testing.T) {
	wrapped := func() error {
		return New(KindCorrupt, "x", NoOffset, "boom")
	}()
	if !IsCorrupt(wrapped) {
		t.Fatal("IsCorrupt should detect fault")
	}
	if fe, ok := AsError(wrapped); !ok || fe.Field != "x" {
		t.Fatal("AsError failed")
	}
	if IsCorrupt(errors.New("plain")) {
		t.Fatal("plain error must not be corrupt")
	}
	if IsCorrupt(New(KindInternal, "x", NoOffset, "boom")) {
		t.Fatal("internal fault must not classify as corrupt")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
