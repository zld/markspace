package fault

import (
	"errors"
	"testing"
)

func TestErrorCarriesOffsetAndStage(t *testing.T) {
	err := Wrap(StageDemux, "mp4", OpBoxOverrun, 1234, "box extends past end", errors.New("eof"))
	off, ok := OffsetOf(err)
	if !ok || off != 1234 {
		t.Fatalf("offset: got %d %v", off, ok)
	}
	if !IsStage(err, StageDemux) {
		t.Fatal("wrong stage")
	}
	fe, ok := As(err)
	if !ok || fe.Operation != OpBoxOverrun || !errors.Is(err, fe.Cause) {
		t.Fatalf("unexpected error: %+v", fe)
	}
	unknown := New(StageMap, "", OpMapping, -1, "duplicate output id")
	if _, ok := OffsetOf(unknown); ok {
		t.Fatal("mapping error must not claim an input offset")
	}
}
