package report

import (
	"strings"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
	"mediasync/internal/stream"
	"mediasync/internal/timeline"
	"mediasync/internal/validate"
)

func fixtureResult() *validate.Result {
	r := &validate.Result{
		Name: "sample-a", Window: clock.FromMilliseconds(2000),
		Sources: []string{"b.srt", "a.mp4"},
		Events: []timeline.Event{
			{Kind: timeline.KindCue, Time: clock.FromMilliseconds(1000), Input: "b.srt", Ref: "cue:1", Offset: -1},
			{Kind: timeline.KindKeyframe, Time: clock.FromMilliseconds(1000), Input: "a.mp4", Ref: "kf:1", Offset: 40},
		},
		Keyframes:  validate.KeyframeStat{Count: 3, Min: 1, Median: 2, Max: 3},
		Intervals:  []stream.Interval{{From: 1, To: 2, DTSGap: 3000, PTSGap: 3000, Samples: 1}},
		MaxTimeDev: clock.Tick(2), MaxByteDev: 7,
		Damaged: []issue.ByteRange{{Input: "a.mp4", Start: 100, End: 120, Categories: []issue.Category{issue.MP4Truncated}}},
		Issues: issue.List{
			issue.New(issue.MP4Truncated, "a.mp4", "cut").WithByte(100),
			issue.New(issue.OverlappingCue, "b.srt", "overlap").WithLine(4),
		},
		Admitted: 2, Recomputed: 1, Sealed: 1,
		CueMaps: []validate.CueMap{{CueIndex: 1, Line: 2, CueStart: clock.FromMilliseconds(1000), Keyframe: 1, TimeDev: clock.Tick(2), ByteDev: 7, Packet: 3, PacketDev: clock.Tick(1)}},
	}
	r.Issues = r.Issues.Sorted()
	first, _ := r.Issues.First()
	r.First = &first
	return r
}

func TestDigestStableAcrossRuns(t *testing.T) {
	a := Digest(BuildSnapshot(fixtureResult()))
	for i := 0; i < 10; i++ {
		b := Digest(BuildSnapshot(fixtureResult()))
		if a != b {
			t.Fatalf("digest changed: %s vs %s", a, b)
		}
	}
	if len(a) != 64 {
		t.Fatalf("digest length %d", len(a))
	}
}

func TestDigestIgnoresAdmissionCounters(t *testing.T) {
	// In-window replay changes arrival counters but must not change the
	// normalized digest: the canonical events are identical.
	a := fixtureResult()
	b := fixtureResult()
	b.Admitted, b.Recomputed, b.Sealed = 99, 42, 0
	if Digest(BuildSnapshot(a)) != Digest(BuildSnapshot(b)) {
		t.Fatal("digest depends on arrival counters")
	}
}

func TestDigestSensitiveToCanonicalContent(t *testing.T) {
	a := fixtureResult()
	b := fixtureResult()
	b.Events[1].Time += 1
	if Digest(BuildSnapshot(a)) == Digest(BuildSnapshot(b)) {
		t.Fatal("digest ignored a canonical time change")
	}
	c := fixtureResult()
	c.Issues = append(c.Issues, issue.New(issue.InvalidTime, "b.srt", "bad").WithLine(9))
	if Digest(BuildSnapshot(a)) == Digest(BuildSnapshot(c)) {
		t.Fatal("digest ignored a new issue")
	}
	d := fixtureResult()
	d.Damaged[0].End += 1
	if Digest(BuildSnapshot(a)) == Digest(BuildSnapshot(d)) {
		t.Fatal("digest ignored a damaged range change")
	}
}

func TestDigestNoAmbiguityAcrossFields(t *testing.T) {
	// Length-prefixed fields must distinguish ("ab","c") from ("a","bc").
	a := Summary{Name: "ab", Window: 0, Sources: []string{"c"}}
	b := Summary{Name: "a", Window: 0, Sources: []string{"bc"}}
	if Digest(a) == Digest(b) {
		t.Fatal("digest collides across field boundaries")
	}
}

func TestRenderDeterministicAndContainsKeyLines(t *testing.T) {
	r := fixtureResult()
	out1 := Render(r)
	out2 := Render(r)
	if out1 != out2 {
		t.Fatal("render not deterministic")
	}
	for _, want := range []string{
		"sample: sample-a", "first failure:", "sha256:", "damaged ranges:",
		"max time deviation:", "cue mapping:", "keyframe intervals", "errors: 2",
	} {
		if !strings.Contains(out1, want) {
			t.Fatalf("render missing %q:\n%s", want, out1)
		}
	}
	if strings.Contains(out1, "202") && strings.Contains(out1, "ago") {
		t.Fatal("render appears to contain wall-clock text")
	}
}

func TestSummarySourcesSorted(t *testing.T) {
	s := BuildSnapshot(fixtureResult())
	if s.Sources[0] != "a.mp4" || s.Sources[1] != "b.srt" {
		t.Fatalf("sources=%v", s.Sources)
	}
}
