package fault_test

import (
	"errors"
	"strings"
	"testing"

	"archivecheck/internal/fault"
)

func TestErrCarriesStageSourceOffset(t *testing.T) {
	err := fault.New(fault.StageMP4, fault.KindTruncated, "clip.mp4", 128, "box extends past end").Span(128, 200)
	if err.Offset != 128 || err.End != 200 {
		t.Fatalf("span not recorded: %+v", err)
	}
	msg := err.Error()
	for _, want := range []string{"mp4/truncated", "source=clip.mp4", "offset=128..200", "box extends past end"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if got := fault.OffsetOf(err); got != 128 {
		t.Errorf("OffsetOf = %d, want 128", got)
	}
	if fault.OffsetOf(errors.New("plain")) != -1 {
		t.Error("plain error should report offset -1")
	}
}

func TestAsUnwrapsWrappedErrors(t *testing.T) {
	inner := fault.New(fault.StageRTP, fault.KindCorrupt, "stream.rtp", 44, "sequence gap")
	wrapped := errors.Join(errors.New("context"), inner)
	got := fault.As(wrapped)
	if got == nil || got.Stage != fault.StageRTP || got.Offset != 44 {
		t.Fatalf("As returned %+v", got)
	}
	if !fault.Is(wrapped, fault.StageRTP, fault.KindCorrupt) {
		t.Error("Is should find rtp/corrupt")
	}
	if !fault.HasKind(wrapped, fault.KindCorrupt) {
		t.Error("HasKind should find corrupt")
	}
	if fault.Is(wrapped, fault.StageMP4, fault.KindCorrupt) {
		t.Error("Is should not match the wrong stage")
	}
}

func TestJoinAndSpans(t *testing.T) {
	a := fault.New(fault.StageSubtitle, fault.KindInvalid, "s.srt", 10, "bad time")
	b := fault.New(fault.StageSubtitle, fault.KindTruncated, "s.srt", 50, "cut").Span(50, 70)
	joined := fault.Join(a, b, nil)
	if joined == nil {
		t.Fatal("Join returned nil")
	}
	spans := fault.Spans(joined)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	if spans[1].Start != 50 || spans[1].End != 70 || spans[1].Kind != fault.KindTruncated {
		t.Errorf("unexpected span %+v", spans[1])
	}
	if fault.Join(nil, nil) != nil {
		t.Error("Join of nils should be nil")
	}
	if got := fault.Join(a); got != a {
		t.Error("Join of one should return it unchanged")
	}
}

func TestWrapSetsKindAndCause(t *testing.T) {
	cause := errors.New("disk gone")
	err := fault.Wrap(cause, fault.StageSource, fault.KindMissing, "a.bin", 0, "open")
	if !errors.Is(err, cause) {
		t.Error("wrapped cause is not reachable")
	}
	if !strings.Contains(err.Error(), "disk gone") {
		t.Errorf("cause missing from %q", err)
	}
}
