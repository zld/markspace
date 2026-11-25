package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorPreservesOffsetAndTrack(t *testing.T) {
	cause := errors.New("checksum mismatch")
	err := New(StageParse, "checksum", 128, 3, cause)
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not discoverable")
	}
	if !strings.Contains(err.Error(), "offset=128") || !strings.Contains(err.Error(), "track=3") {
		t.Fatalf("error %q lacks coordinates", err)
	}

	f, ok := As(err)
	if !ok {
		t.Fatal("fault metadata not extracted")
	}
	if f.Stage != StageParse || f.Offset != 128 || f.TrackID != 3 || f.Code != "checksum" {
		t.Fatalf("unexpected failure: %+v", f)
	}
}

func TestAsOrdinaryError(t *testing.T) {
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("ordinary error reported as fault")
	}
}
