package track

import (
	"testing"

	"mediamerge/internal/fixgen"
	"mediamerge/internal/ingest"
	"mediamerge/internal/model"
)

func parse(t *testing.T, name string, spec fixgen.FileSpec) *ingest.File {
	t.Helper()
	errs := &model.Errors{}
	f, fatal := ingest.ParseFile(name, fixgen.Build(spec), errs)
	if fatal != nil {
		t.Fatalf("parse %s: %v", name, fatal)
	}
	return f
}

func videoTrack(id uint32, color *fixgen.ColorSpec) fixgen.TrackSpec {
	return fixgen.TrackSpec{ID: id, Handler: "vide", Timescale: 90000,
		Width: 1920, Height: 1080, Color: color}
}

func soundTrack(id uint32, ch uint16, rate uint32) fixgen.TrackSpec {
	return fixgen.TrackSpec{ID: id, Handler: "soun", Timescale: rate,
		Channels: ch, SampleRate: rate}
}

func TestBuildGraphLinksAudioToVideo(t *testing.T) {
	f := parse(t, "g.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		videoTrack(1, nil), soundTrack(2, 2, 48000),
	}})
	g := BuildGraph([]*ingest.File{f})
	if got := g.Dependencies(2); len(got) != 1 || got[0] != 1 {
		t.Fatalf("audio deps=%v", got)
	}
	if got := g.Dependencies(1); len(got) != 0 {
		t.Fatalf("video deps=%v", got)
	}
}

func TestNormalizeRejectsCycle(t *testing.T) {
	f := parse(t, "c.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		soundTrack(2, 2, 48000), soundTrack(3, 2, 48000),
	}})
	g := NewGraph()
	g.AddTrack(2)
	g.AddTrack(3)
	g.AddEdge(2, 3)
	g.AddEdge(3, 2)
	var errs model.Errors
	if _, err := Normalize([]*ingest.File{f}, g, &errs); err == nil {
		t.Fatal("cycle not rejected")
	}
	if errs.MaxLevel() != model.LevelFatal {
		t.Fatalf("level=%v", errs.MaxLevel())
	}
}

func TestNormalizeChannelConflictWithoutMatrix(t *testing.T) {
	// Same track id, 2ch in the high-priority file, 6ch in the second.
	f1 := parse(t, "a.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{soundTrack(2, 2, 48000)}})
	f2 := parse(t, "b.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{soundTrack(2, 6, 48000)}})
	var errs model.Errors
	n, fatal := Normalize([]*ingest.File{f1, f2}, BuildGraph([]*ingest.File{f1, f2}), &errs)
	if fatal != nil {
		t.Fatalf("fatal: %v", fatal)
	}
	md := n.ByID(2)
	if md == nil {
		t.Fatal("track missing")
	}
	if md.Mixable {
		t.Fatal("2->6 channels has no explicit matrix; must not be mixable")
	}
	if md.ChannelPlan == nil || md.ChannelPlan.TargetChannels != 2 {
		t.Fatalf("original layout not retained: %+v", md.ChannelPlan)
	}
	if len(md.Conflicts) == 0 {
		t.Fatal("conflict not recorded")
	}
}

func TestNormalizeExplicitMonoToStereo(t *testing.T) {
	// Priority source is mono; a second source declares stereo. The only
	// permitted conversion is the documented mono->L/R duplication matrix.
	f1 := parse(t, "mono.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{soundTrack(2, 1, 48000)}})
	f2 := parse(t, "st.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{soundTrack(2, 2, 48000)}})
	var errs model.Errors
	n, fatal := Normalize([]*ingest.File{f1, f2}, BuildGraph([]*ingest.File{f1, f2}), &errs)
	if fatal != nil {
		t.Fatalf("fatal: %v", fatal)
	}
	md := n.ByID(2)
	plan := md.ChannelPlan
	if plan == nil || plan.SourceChannels != 1 || plan.TargetChannels != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if !plan.Mixable || len(plan.Matrix) != 2 || len(plan.Matrix[0]) != 1 {
		t.Fatalf("explicit matrix not applied: %+v", plan)
	}
	if !md.Mixable {
		t.Fatal("documented matrix must keep the track mixable")
	}
}

func TestNormalizeColorConflictKeepsPrioritySource(t *testing.T) {
	hi := &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}
	lo := &fixgen.ColorSpec{Primaries: 9, Transfer: 16, Matrix: 9}
	f1 := parse(t, "hi.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{videoTrack(1, hi)}})
	f2 := parse(t, "lo.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{videoTrack(1, lo)}})
	var errs model.Errors
	n, fatal := Normalize([]*ingest.File{f1, f2}, BuildGraph([]*ingest.File{f1, f2}), &errs)
	if fatal != nil {
		t.Fatalf("fatal: %v", fatal)
	}
	md := n.ByID(1)
	if md.Color == nil || md.Color.Primaries != 1 || md.Source != "hi.mp4" {
		t.Fatalf("colour decision: %+v from %s", md.Color, md.Source)
	}
	if len(md.ColorOverrides) != 1 || md.ColorOverrides[0].Primaries != 9 {
		t.Fatalf("override not recorded: %+v", md.ColorOverrides)
	}
	// The lower-priority file must appear in the diagnostics.
	found := false
	for _, e := range errs.All() {
		if e.SourceFile == "lo.mp4" && e.TrackID == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("override diagnostic missing")
	}
}

func TestNormalizeResamplePlanIsRational(t *testing.T) {
	f1 := parse(t, "441.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		videoTrack(1, nil), soundTrack(2, 2, 44100),
	}})
	f2 := parse(t, "48.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		videoTrack(1, nil), soundTrack(3, 2, 48000),
	}})
	var errs model.Errors
	n, fatal := Normalize([]*ingest.File{f1, f2}, BuildGraph([]*ingest.File{f1, f2}), &errs)
	if fatal != nil {
		t.Fatalf("fatal: %v", fatal)
	}
	md := n.ByID(2)
	if md.Resample == nil {
		t.Fatal("no resample plan for 44100 track")
	}
	if md.Resample.ToRate != 48000 || md.Resample.Up != 160 || md.Resample.Down != 147 {
		t.Fatalf("ratio=%d/%d", md.Resample.Up, md.Resample.Down)
	}
	if !md.Resample.PlannedOnly {
		t.Fatal("resample must be marked plan-only")
	}
	frames, frac := md.Resample.FramesFor(model.NsDuration(model.NsPerSecond))
	if frames != 48000 || frac != 0 {
		t.Fatalf("frames=%d frac=%v", frames, frac)
	}
}

func TestNormalizeDeterministic(t *testing.T) {
	hi := &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}
	f1 := parse(t, "hi.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		videoTrack(1, hi), soundTrack(2, 2, 48000)}})
	f2 := parse(t, "lo.mp4", fixgen.FileSpec{Tracks: []fixgen.TrackSpec{
		videoTrack(1, &fixgen.ColorSpec{Primaries: 9, Transfer: 9, Matrix: 9}),
		soundTrack(2, 2, 44100)}})
	run := func() *Normalized {
		var errs model.Errors
		n, _ := Normalize([]*ingest.File{f1, f2}, BuildGraph([]*ingest.File{f1, f2}), &errs)
		return n
	}
	a, b := run(), run()
	if len(a.Tracks) != len(b.Tracks) {
		t.Fatal("track count differs")
	}
	for i := range a.Tracks {
		if a.Tracks[i].ID != b.Tracks[i].ID || a.Tracks[i].Mixable != b.Tracks[i].Mixable ||
			len(a.Tracks[i].ColorOverrides) != len(b.Tracks[i].ColorOverrides) {
			t.Fatalf("track %d differs", i)
		}
	}
}
