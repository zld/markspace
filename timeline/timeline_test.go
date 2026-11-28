package timeline

import (
	"errors"
	"math"
	"testing"
)

func TestRationalExactArithmetic(t *testing.T) {
	a := MustRational(1, 3)
	b := MustRational(1, 6)
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sum, MustRational(1, 2); got != want {
		t.Fatalf("1/3+1/6 = %s, want %s", got, want)
	}
	diff, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := diff, MustRational(1, 6); got != want {
		t.Fatalf("1/3-1/6 = %s, want %s", got, want)
	}
	// A tenth of a millisecond difference must survive exactly: float64 would
	// not represent it at hour-scale timestamps.
	big1 := MustRational(36_000_000_000, 1_000_000) // 10 h
	small := MustRational(1, 10_000_000)            // 0.1 µs
	got, err := big1.Add(small)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(big1) == 0 {
		t.Fatalf("sum %s should differ from %s: sub-microsecond detail was lost", got, big1)
	}
	if got != MustRational(360_000_000_001, 10_000_000) {
		t.Fatalf("sum = %s, want exactly 360000000001/10000000", got)
	}
}

func TestRationalRejectsZeroDenominator(t *testing.T) {
	_, err := NewRational(1, 0)
	if CodeOf(err) != CodeInvalidTime {
		t.Fatalf("code = %q, want %q (err=%v)", CodeOf(err), CodeInvalidTime, err)
	}
}

func TestRationalRoundingHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		r    Rational
		want int64
	}{
		{MustRational(1, 1_000_000), 1},   // exactly 1 µs
		{MustRational(1, 3_000_000), 0},   // 0.33 µs rounds down
		{MustRational(2, 3_000_000), 1},   // 0.67 µs rounds up
		{MustRational(-1, 2_000_000), -1}, // exactly -0.5 µs rounds away from zero
		{MustRational(5, 2_000_000), 3},   // 2.5 µs rounds to 3
		{MustRational(-5, 2_000_000), -3}, // -2.5 µs rounds to -3
		{MustRational(0, 7), 0},
	}
	for _, c := range cases {
		got, err := c.r.Micros()
		if err != nil {
			t.Fatalf("%s: %v", c.r, err)
		}
		if got != c.want {
			t.Errorf("%s micros = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestTimestampSeconds(t *testing.T) {
	ts := Timestamp{Ticks: 90000, Base: Base{Name: "v", TimeScale: 90000}}
	got, err := ts.Seconds()
	if err != nil {
		t.Fatal(err)
	}
	if got != MustRational(1, 1) {
		t.Fatalf("90000/90000 = %s", got)
	}
	// 1001/30000 ticks is an NTSC frame: exactly 1001/30000 s.
	ntsc := Timestamp{Ticks: 1001, Base: Base{Name: "v", TimeScale: 30000}}
	got, err = ntsc.Seconds()
	if err != nil {
		t.Fatal(err)
	}
	if got != MustRational(1001, 30000) {
		t.Fatalf("NTSC frame = %s", got)
	}
}

func TestTickToAcrossBases(t *testing.T) {
	video := Base{Name: "v", TimeScale: 90000}
	audio := Base{Name: "a", TimeScale: 48000}
	// One second of video ticks maps to exactly one second of audio ticks.
	got, err := video.TickTo(90000, audio)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 48000 || got.Base != audio {
		t.Fatalf("got %+v", got)
	}
	// One NTSC frame in 90 kHz video (3003 ticks) maps to 1601.6 audio ticks,
	// which must round deterministically to 1602.
	got, err = video.TickTo(3003, audio)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 1602 {
		t.Fatalf("3003/90000 s in 48 kHz = %d ticks, want 1602", got.Ticks)
	}
}

func TestBaseValidation(t *testing.T) {
	if err := (Base{Name: "bad"}).Validate(); CodeOf(err) != CodeInvalidTime {
		t.Fatalf("zero timescale: %v", err)
	}
	if err := (Base{Name: "bad", TimeScale: 100, WrapAt: 100}).Validate(); err == nil {
		t.Fatal("wrap modulus equal to timescale must be rejected")
	}
}

func TestUnwrapForwardWrap(t *testing.T) {
	u, err := NewUnwrapper(Base{Name: "rtp", TimeScale: 90000, WrapAt: 1 << 32})
	if err != nil {
		t.Fatal(err)
	}
	// Jump from just below the wrap point to just above zero.
	first, _, _ := u.Push((1 << 32) - 100)
	second, status, err := u.Push(50)
	if err != nil {
		t.Fatal(err)
	}
	if status != WrapStatusWrapped {
		t.Fatalf("status = %v, want wrapped", status)
	}
	if want := int64((1 << 32) - 100 + 150); second != want {
		t.Fatalf("continuous = %d, want %d", second, want)
	}
	if u.WrapCount != 1 {
		t.Fatalf("wrap count = %d", u.WrapCount)
	}
	_ = first
}

func TestUnwrapLatePacketBeforeWrap(t *testing.T) {
	u, _ := NewUnwrapper(Base{Name: "rtp", TimeScale: 90000, WrapAt: 1 << 32})
	u.Push((1 << 32) - 10) // last packet of epoch 0
	u.Push(5)              // first packet of epoch 1: continuous = 2^32+5
	// A late packet from epoch 0 arrives after the wrap: raw value near 2^32
	// must map onto epoch 0, not the distant future of epoch 1.
	got, status, err := u.Push((1 << 32) - 2)
	if err != nil {
		t.Fatal(err)
	}
	if status != WrapStatusReordered {
		t.Fatalf("status = %v, want reordered", status)
	}
	if want := int64((1 << 32) - 2); got != want {
		t.Fatalf("continuous = %d, want %d", got, want)
	}
}

func TestUnwrapReorderAndDuplicateWithinEpoch(t *testing.T) {
	u, _ := NewUnwrapper(Base{Name: "ts", TimeScale: 90000})
	u.Push(1000)
	got, status, err := u.Push(900)
	if err != nil {
		t.Fatal(err)
	}
	if status != WrapStatusReordered || got != 900 {
		t.Fatalf("reorder: got %d %v", got, status)
	}
	if _, status, _ := u.Push(900); status != WrapStatusSameTime {
		t.Fatalf("repeat status = %v, want same-time", status)
	}
	if u.DuplicateCount != 1 || u.ReorderCount != 1 {
		t.Fatalf("reorder=%d duplicate=%d", u.ReorderCount, u.DuplicateCount)
	}
}

func TestUnwrapAmbiguousHalfWindowIsReportedNotGuessed(t *testing.T) {
	u, _ := NewUnwrapper(Base{Name: "rtp", TimeScale: 90000, WrapAt: 1 << 32})
	u.Push(10)
	_, _, err := u.Push(10 + (1 << 31))
	if CodeOf(err) != CodeAmbiguousWrap {
		t.Fatalf("code = %q, want %q (err=%v)", CodeOf(err), CodeAmbiguousWrap, err)
	}
	issue, ok := IssueOf(err)
	if !ok || issue.Ticks != 10+(1<<31) {
		t.Fatalf("issue should carry the raw ambiguous tick, got %+v", issue)
	}
}

func TestUnwrapRejectsOutOfDomain(t *testing.T) {
	u, _ := NewUnwrapper(Base{Name: "rtp", TimeScale: 90000, WrapAt: 1 << 32})
	_, _, err := u.Push(1 << 32)
	if CodeOf(err) != CodeInvalidTime {
		t.Fatalf("code = %q", CodeOf(err))
	}
	if _, _, err := u.Push(-1); CodeOf(err) != CodeInvalidTime {
		t.Fatalf("negative raw tick accepted")
	}
}

func TestIssueLocationRendering(t *testing.T) {
	err := newLocated(CodeTruncated, "a.mp4", 4096, 90000, "emsg length %d exceeds box", 12)
	var le *LocatedError
	if !errors.As(err, &le) {
		t.Fatal("expected LocatedError")
	}
	le.Issue.TrackID = 2
	le.Issue.ByteOffset = 4096
	want := `error[truncated] file=a.mp4 offset=4096 track=2 ticks=4096/90000 emsg length 12 exceeds box`
	if got := err.Error(); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestIssuesDedupeAndSort(t *testing.T) {
	var is Issues
	is.Add(Issue{Code: CodeTruncated, File: "b.mp4", ByteOffset: 10, Message: "x"})
	is.Add(Issue{Code: CodeTruncated, File: "a.mp4", ByteOffset: 20, Message: "y"})
	is.Add(Issue{Code: CodeTruncated, File: "b.mp4", ByteOffset: 10, Message: "x"})
	is.Add(Issue{Severity: SeverityWarning, Code: CodeDuplicate, File: "a.mp4", ByteOffset: 1, Message: "w"})
	if len(is) != 3 {
		t.Fatalf("len = %d, want 3 after dedupe", len(is))
	}
	sorted := is.Sorted()
	if sorted[0].File != "a.mp4" || sorted[0].ByteOffset != 1 {
		t.Fatalf("sort order wrong: %+v", sorted)
	}
	if !is.HasErrors() {
		t.Fatal("truncated issues are fatal")
	}
	var warnings Issues
	warnings.Add(Issue{Severity: SeverityWarning, Code: CodeDuplicate, Message: "only a warning"})
	if warnings.HasErrors() {
		t.Fatal("warnings are not fatal")
	}
}

func TestEventSetDedupeAndConflicts(t *testing.T) {
	base := Base{Name: "sub", TimeScale: 1000}
	mk := func(ticks int64, payload string, off int64) Event {
		ev, err := NewEvent(KindSubtitle, Timestamp{Ticks: ticks, Base: base},
			MustRational(2, 1), "sub", []byte(payload), Source{File: "s.mkv", ByteOffset: off})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	var set EventSet
	set.Add(mk(1000, "hello", 10))
	set.Add(mk(1000, "hello", 90))  // same instant, same text: duplicate
	set.Add(mk(1000, "other", 120)) // same instant, different text: conflict
	set.Add(mk(2000, "later", 200))
	if set.Len() != 2 {
		t.Fatalf("len = %d, want 2", set.Len())
	}
	if set.Duplicates() != 1 || set.Conflicts() != 1 {
		t.Fatalf("dupes=%d conflicts=%d", set.Duplicates(), set.Conflicts())
	}
	if !set.Issues().HasErrors() {
		t.Fatal("conflict must be fatal")
	}
	set.Sort()
	if ms, _ := set.Events[0].Presentation.Micros(); ms != 1_000_000 {
		t.Fatalf("first event at %d µs", ms)
	}
}

func TestEventSetSortIsStableAcrossTracks(t *testing.T) {
	// Two renditions emitting the same cue at the same instant must sort by
	// source so the manifest digest matches whichever track order the scan
	// discovered them in.
	mk := func(file string) Event {
		ev, _ := NewEvent(KindChapter, Timestamp{Ticks: 0, Base: Base{Name: "x", TimeScale: 1000}},
			Rational{}, "ch", []byte("intro"), Source{File: file, ByteOffset: 1})
		return ev
	}
	a := EventSet{}
	a.Add(mk("z.mp4"))
	a.Add(mk("a.mp4"))
	b := EventSet{}
	b.Add(mk("a.mp4"))
	b.Add(mk("z.mp4"))
	a.Sort()
	b.Sort()
	if a.Events[0].Source.File != "a.mp4" || b.Events[0].Source.File != "a.mp4" {
		t.Fatal("sort must be independent of insertion order")
	}
}

func TestMillisUsesRoundingNotTruncation(t *testing.T) {
	// 7.6 ms must report as 8 ms; silent truncation would break the av-sync
	// budget check in the demo.
	ms, err := MustRational(76, 10_000).Millis()
	if err != nil {
		t.Fatal(err)
	}
	if ms != 8 {
		t.Fatalf("7.6 ms = %d", ms)
	}
	if math.Abs(MustRational(1001, 30000).Float64()*1000-33.3667) > 1e-3 {
		t.Fatal("float reporting drifted")
	}
}
