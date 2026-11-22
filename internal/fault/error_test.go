package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestLocatedErrorFormattingAndChain(t *testing.T) {
	base := New("mp4.parse", KindParse, ErrBadSample).
		WithPath("/x/a.mp4").At(4096).WithTrack(2).WithSample(17).
		WithDetail("size %d exceeds mdat", 99).Recovered()

	if !errors.Is(base, ErrBadSample) {
		t.Fatalf("sentinel cause not preserved")
	}
	if !errors.Is(base, &Error{Kind: KindParse, Err: ErrBadSample}) {
		t.Fatalf("located error equality failed")
	}
	msg := base.Error()
	for _, want := range []string{`file="/x/a.mp4"`, "offset=4096", "track=2", "sample=17", "[recoverable]"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q misses %q", msg, want)
		}
	}
	if base.Offset != 4096 || base.TrackID != 2 || base.Sample != 17 {
		t.Fatalf("builder mutated or lost fields: %+v", base)
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"clean", nil, 0},
		{"recovered", New("x", KindTruncated, ErrTruncated).Recovered(), 1},
		{"verify", New("x", KindVerify, ErrIncomplete), 2},
		{"index-fatal", New("x", KindIndex, ErrIncomplete).Fatal(), 2},
		{"index-recovered", New("x", KindIndex, ErrIncomplete).Recovered(), 1},
		{"plain", errors.New("boom"), 2},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("%s: ExitCode=%d want %d", tc.name, got, tc.want)
		}
	}
}

func TestKindOf(t *testing.T) {
	err := errors.Join(New("a", KindAudio, nil).Recovered(), New("b", KindParse, nil))
	if got := KindOf(err); got != KindAudio {
		t.Fatalf("KindOf=%q", got)
	}
}
