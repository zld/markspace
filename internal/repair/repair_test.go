package repair

import (
	"testing"

	"archivist/internal/clock"
	"archivist/internal/fault"
	"archivist/internal/fixture"
	"archivist/internal/scan"
)

func scanSample(t *testing.T, s *fixture.Sample) *scan.Container {
	t.Helper()
	r, err := scan.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Scan()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRepairNormalUnchanged(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 20
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	if got := res.TotalRepaired(); got != 0 {
		t.Fatalf("repaired = %d", got)
	}
	if err := VerifyMonotonic(res); err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	for i, e := range tr.Entries {
		if e.Repaired != int64(i)*3000 {
			t.Fatalf("sample %d repaired = %d", i, e.Repaired)
		}
		if e.Kind != KindUnchanged {
			t.Fatalf("sample %d kind = %s", i, e.Kind)
		}
		wantMovie := (int64(i)*3000*1000 + 45000) / 90000
		if e.MovieTime != wantMovie {
			t.Fatalf("sample %d movie time = %d want %d", i, e.MovieTime, wantMovie)
		}
	}
}

func TestRepairWrap(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("w", fixture.KindWrap)
	opts.SampleCount = 60
	opts.FirstPTS = 0xFFFFD8F0
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if err := VerifyMonotonic(res); err != nil {
		t.Fatal(err)
	}
	// Exactly one sample crosses the boundary, and it is flagged as a wrap.
	wraps := 0
	for _, e := range tr.Entries {
		if e.Kind == KindWrap {
			wraps++
		}
	}
	if wraps != 1 {
		t.Fatalf("wrap entries = %d", wraps)
	}
	// Repaired values must continue the pre-wrap cadence.
	start := int64(0xFFFFD8F0)
	for i, e := range tr.Entries {
		if e.Repaired != start+int64(i)*3000 {
			t.Fatalf("sample %d repaired = %d want %d", i, e.Repaired, start+int64(i)*3000)
		}
	}
	// The wrap sample's raw value is small, so the correction is large.
	wrapEntry := tr.Entries[0]
	for _, e := range tr.Entries {
		if e.Kind == KindWrap {
			wrapEntry = e
		}
	}
	if wrapEntry.Raw > 0x1000 {
		t.Fatalf("wrap sample raw = %#x", wrapEntry.Raw)
	}
	if wrapEntry.Delta <= 0 {
		t.Fatalf("wrap correction = %d", wrapEntry.Delta)
	}
	if wrapEntry.Original == wrapEntry.Repaired {
		t.Fatal("wrap must record a correction")
	}
	if wrapEntry.Delta < int64(clock.Wrap)/2 {
		t.Fatalf("wrap delta = %d, expected a ~2^32 shift", wrapEntry.Delta)
	}
	if wrapEntry.Original+wrapEntry.Delta != wrapEntry.Repaired {
		t.Fatal("wrap correction does not add up")
	}
}

func TestRepairBackward(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("b", fixture.KindRegression)
	opts.SampleCount = 20
	opts.FirstPTS = 89000
	s, err := fixture.BuildDegraded(dir, opts, fixture.Degrade{AtSample: 7, Mode: fixture.DegradeBackward, Amount: 6000})
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if err := VerifyMonotonic(res); err != nil {
		t.Fatal(err)
	}
	// Samples 0..6 keep their original values.
	for i := 0; i < 7; i++ {
		if tr.Entries[i].Kind != KindUnchanged {
			t.Fatalf("sample %d kind = %s", i, tr.Entries[i].Kind)
		}
	}
	// Samples 7.. step back by 6000 and are clamped to prev+step.
	for i := 7; i < 20; i++ {
		e := tr.Entries[i]
		if e.Kind != KindBackward {
			t.Fatalf("sample %d kind = %s", i, e.Kind)
		}
		// Once the timeline steps back, each following sample is pulled to
		// exactly one step after the previous repaired value.
		want := 89000 + int64(i-7)*3000 + 21000
		if e.Repaired != want {
			t.Fatalf("sample %d repaired = %d want %d", i, e.Repaired, want)
		}
		if e.Delta <= 0 {
			t.Fatalf("sample %d delta = %d", i, e.Delta)
		}
		if e.Original != int64(e.Raw) {
			t.Fatalf("sample %d raw/original mismatch %d/%d", i, e.Raw, e.Original)
		}
		if e.Original+e.Delta != e.Repaired {
			t.Fatalf("sample %d correction does not add up", i)
		}
	}
}

func TestRepairDuplicate(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("d", fixture.KindDuplicate)
	opts.SampleCount = 12
	opts.FirstPTS = 5000
	s, err := fixture.BuildDegraded(dir, opts, fixture.Degrade{AtSample: 4, Mode: fixture.DegradeDuplicate})
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if err := VerifyMonotonic(res); err != nil {
		t.Fatal(err)
	}
	if tr.Entries[4].Kind != KindDuplicate {
		t.Fatalf("sample 4 kind = %s", tr.Entries[4].Kind)
	}
	if tr.Entries[4].Delta != 3000 {
		t.Fatalf("duplicate delta = %d", tr.Entries[4].Delta)
	}
	// The following sample returns to its natural position; because the
	// duplicate consumed one step, it must still be strictly after sample 4.
	if tr.Entries[5].Repaired <= tr.Entries[4].Repaired {
		t.Fatal("timeline not strictly increasing after duplicate")
	}
}

func TestRepairWrapRegression(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("wr", fixture.KindCombined)
	opts.SampleCount = 50
	opts.FirstPTS = 0xFFFFF000
	s, err := fixture.BuildDegraded(dir, opts, fixture.Degrade{AtSample: 20, Mode: fixture.DegradeBackward, Amount: 9000})
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyMonotonic(res); err != nil {
		t.Fatal(err)
	}
	var wraps, backwards int
	for _, e := range res.Tracks[0].Entries {
		switch e.Kind {
		case KindWrap:
			wraps++
		case KindBackward:
			backwards++
		}
	}
	if wraps == 0 {
		t.Fatal("expected a wrap correction")
	}
	if backwards != 50-20 {
		t.Fatalf("backward corrections = %d", backwards)
	}
}

func TestRepairAmbiguousBackwardStops(t *testing.T) {
	// A backward jump well beyond one second cannot be repaired safely.
	dir := t.TempDir()
	opts := fixture.DefaultOptions("amb", fixture.KindRegression)
	opts.SampleCount = 10
	opts.FirstPTS = 800000
	s, err := fixture.BuildDegraded(dir, opts, fixture.Degrade{AtSample: 3, Mode: fixture.DegradeBackward, Amount: 200000})
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	_, err = Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err == nil {
		t.Fatal("expected ambiguous repair to fail")
	}
	f, ok := fault.AsFault(err)
	if !ok || f.Kind != fault.KindTimestamp {
		t.Fatalf("err = %v", err)
	}
	if f.TrackID != 1 || f.Timescale != 90000 {
		t.Fatalf("fault track context = %+v", f)
	}
	if f.Offset < 0 {
		t.Fatalf("fault offset = %d", f.Offset)
	}
	if fault.ExitCode(err) != 2 {
		t.Fatalf("exit = %d", fault.ExitCode(err))
	}
}

func TestMovieTimebaseMatchesMovieTimeline(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("m", fixture.KindStartOffset)
	opts.SampleCount = 30
	opts.FirstPTS = 270000
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := scanSample(t, s)
	res, err := Repair(c, DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if tr.Entries[0].MovieTime != 3000 {
		t.Fatalf("first movie time = %d", tr.Entries[0].MovieTime)
	}
	if tr.Entries[1].MovieTime-tr.Entries[0].MovieTime != 33 {
		t.Fatalf("movie step = %d", tr.Entries[1].MovieTime-tr.Entries[0].MovieTime)
	}
}
