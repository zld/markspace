package pipeline

import (
	"errors"
	"testing"

	"archive/internal/demux"
	"archive/internal/fault"
	"archive/internal/fixture"
	"archive/internal/model"
	"archive/internal/probe"
	"archive/internal/remux"
	"archive/internal/verify"
)

func mixedSources(t *testing.T) []Source {
	t.Helper()
	var out []Source
	for _, spec := range fixture.DefaultMixed() {
		var data []byte
		var err error
		if spec.Format == model.ContainerMP4 {
			_, data, err = fixture.BuildMP4(spec)
		} else {
			_, data, err = fixture.BuildMXF(spec)
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, Source{Name: spec.Name, Format: spec.Format, Data: data})
	}
	return out
}

func ingestAll(t *testing.T, sources []Source) []*Ingested {
	t.Helper()
	var ins []*Ingested
	for _, s := range sources {
		in, err := Ingest(s)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		ins = append(ins, in)
	}
	return ins
}

// selectedAsInput converts the selected tracks back into a demux.Result so the
// verifier can compare input and output through the same interface.
func selectedAsInput(run *Run) *demux.Result {
	res := &demux.Result{}
	for _, logical := range run.Map.Order {
		if td := run.Selected[logical]; td != nil && len(td.Packets) > 0 {
			res.Tracks = append(res.Tracks, td)
		}
	}
	return res
}

func TestPipelineEndToEndLossless(t *testing.T) {
	ins := ingestAll(t, mixedSources(t))
	run, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Map.Tracks) != 3 {
		t.Fatalf("mapped tracks = %d (%v)", len(run.Map.Tracks), run.Map.Order)
	}
	for _, mt := range run.Map.Tracks {
		if mt.Logical != "video" {
			continue
		}
		if mt.Color.Primaries.Value != "bt709" || mt.Color.Primaries.Source != "mxf" {
			t.Fatalf("video primaries = %+v", mt.Color.Primaries)
		}
		if len(mt.Color.Conflicts) == 0 {
			t.Fatal("expected MP4 colour conflicts")
		}
	}
	if run.MappedWithPayload() != 3 {
		t.Fatalf("mapped_with_payload = %d", run.MappedWithPayload())
	}
	out, err := Remux(run, remux.Options{Workers: 3})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Check(selectedAsInput(run), out.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("verify failed: %s %v", rep.Summary, FaultLines(rep.Faults))
	}
	// The payload digest must be identical before and after.
	outSrc := probe.Source{Name: "out", Data: out.Data}
	outFile, err := probe.Probe(outSrc, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	outDemux, err := demux.Demux(outSrc, outFile)
	if err != nil {
		t.Fatal(err)
	}
	if PayloadDigest(run.Selected) != PayloadDigestOfResult(outDemux) {
		t.Fatal("payload digest changed across remux")
	}
}

func TestPipelineRecoversMissingIndex(t *testing.T) {
	sources := mixedSources(t)
	// Drop the MP4 sample tables and the MXF index segments.
	for i := range sources {
		spec, _ := fixture.FindSpec(fixture.DefaultMixed(), sources[i].Name)
		for j := range spec.Tracks {
			spec.Tracks[j].DropsIndex = true
		}
		var data []byte
		var err error
		if spec.Format == model.ContainerMP4 {
			_, data, err = fixture.BuildMP4(spec)
		} else {
			_, data, err = fixture.BuildMXF(spec)
		}
		if err != nil {
			t.Fatal(err)
		}
		sources[i].Data = data
	}
	ins := ingestAll(t, sources)
	for _, in := range ins {
		if len(in.Scans) == 0 {
			t.Fatalf("%s: nothing was scanned", in.Path)
		}
		for key, scan := range in.Scans {
			if scan.Complete == 0 {
				t.Fatalf("%s/%s: scan recovered nothing", in.Path, key)
			}
		}
	}
	run, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	if run.MappedWithPayload() != 3 {
		t.Fatalf("mapped = %d", run.MappedWithPayload())
	}
	out, err := Remux(run, remux.Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Check(selectedAsInput(run), out.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("verify failed after recovery: %s", rep.Summary)
	}
}

func TestPipelineReportsInjectedDamage(t *testing.T) {
	sources := mixedSources(t)
	// Damage the MXF: cut the tail and corrupt a payload.
	spec, _ := fixture.FindSpec(fixture.DefaultMixed(), "reel_a")
	layout, data, err := fixture.BuildMXF(spec)
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := fixture.Plan(layout, []fixture.DamageKind{fixture.DamageSegment, fixture.DamageTruncate})
	if err != nil {
		t.Fatal(err)
	}
	damaged, injections, err := fixture.Apply(layout, data, plan)
	if err != nil {
		t.Fatal(err)
	}
	for i := range sources {
		if sources[i].Name == "reel_a" {
			sources[i].Data = damaged
		}
	}
	ins := ingestAll(t, sources)
	var all fault.List
	for _, in := range ins {
		all = append(all, in.Faults...)
	}
	all = all.Dedup()
	if all.ByCode(fault.CorruptSegment) == 0 {
		t.Fatalf("segment damage not reported: %v", FaultLines(all))
	}
	if all.ByCode(fault.TruncatedBox) == 0 {
		t.Fatalf("truncation not reported: %v", FaultLines(all))
	}
	// Every injection must be covered by some reported range.
	for _, inj := range injections {
		if !inj.HasRange {
			continue
		}
		covered := false
		for _, e := range all {
			if e.HasRange && e.StartNS <= inj.StartNS && e.EndNS >= inj.EndNS {
				covered = true
				break
			}
		}
		if !covered {
			t.Fatalf("injection %+v not covered by faults", inj)
		}
	}
	// Recovery still yields usable frames from the undamaged region.
	run, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	if run.MappedWithPayload() == 0 {
		t.Fatal("nothing recoverable after damage")
	}
}

func TestPipelineInterruptionResumeMatchesFullRun(t *testing.T) {
	ins := ingestAll(t, mixedSources(t))
	run, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Remux(run, remux.Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an interruption on the first video track.
	var committed []remux.CommittedFragment
	part, err := Remux(run, remux.Options{
		Workers:   1,
		FailTrack: first.Tracks[0].Key,
		FailAfter: 1,
		OnCommit: func(_ remux.TrackResult, cf remux.CommittedFragment) error {
			committed = append(committed, cf)
			return nil
		},
	})
	if !errors.Is(err, remux.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if len(part.Data) != 0 {
		t.Fatal("interrupted run produced output")
	}
	cheats := map[string][]remux.CommittedFragment{}
	for _, cf := range committed {
		cheats[cf.TrackKey] = append(cheats[cf.TrackKey], cf)
	}
	resumed, err := Remux(run, remux.Options{Workers: 1, Committed: cheats})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SHA256 != first.SHA256 {
		t.Fatalf("resume digest %s != full %s", resumed.SHA256, first.SHA256)
	}
	// No fragment written twice.
	seen := map[string]bool{}
	for _, tr := range resumed.Tracks {
		for _, cf := range tr.Committed {
			key := cf.TrackKey + "/" + itoa(cf.Seq)
			if seen[key] {
				t.Fatalf("fragment %s committed twice", key)
			}
			seen[key] = true
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestPipelineMissingRequiredTrackFailsWithoutOutput(t *testing.T) {
	// Only the MXF that has video/audio/data: remove audio from both inputs.
	sources := mixedSources(t)
	for i := range sources {
		spec, _ := fixture.FindSpec(fixture.DefaultMixed(), sources[i].Name)
		var kept []fixture.TrackSpec
		for _, ts := range spec.Tracks {
			if ts.Kind != model.KindAudio {
				kept = append(kept, ts)
			}
		}
		spec.Tracks = kept
		var data []byte
		var err error
		if spec.Format == model.ContainerMP4 {
			_, data, err = fixture.BuildMP4(spec)
		} else {
			_, data, err = fixture.BuildMXF(spec)
		}
		if err != nil {
			t.Fatal(err)
		}
		sources[i].Data = data
	}
	ins := ingestAll(t, sources)
	run, err := Map(ins)
	if err == nil || run != nil {
		t.Fatalf("expected missing audio to fail, got %+v", run)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.MissingRequiredTrack {
		t.Fatalf("error = %v", err)
	}
}

func TestPipelineCycleFails(t *testing.T) {
	sources := mixedSources(t)
	// Represent the cycle through the mapper's declared dependencies, since
	// the fixture's own dependency edges are acyclic by construction.
	ins := ingestAll(t, sources)
	files := make([]*probe.File, 0, len(ins))
	for _, in := range ins {
		files = append(files, in.File)
	}
	_ = files
	// Directly exercise the mapper's cycle path with a synthetic option.
	opts := mapOptions()
	opts.Dependencies = map[string][]string{"video": {"audio"}, "audio": {"video"}}
	// Re-run Map through the internal helper by building files and calling
	// maptrack, mirroring pipeline.Map.
	if _, err := mapWith(files, opts); err == nil {
		t.Fatal("expected cycle to fail")
	} else {
		var fe *fault.Error
		if !errors.As(err, &fe) || fe.Code != fault.DependencyCycle {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestPayloadDigestDeterministic(t *testing.T) {
	ins := ingestAll(t, mixedSources(t))
	run, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	a := PayloadDigest(run.Selected)
	run2, err := Map(ins)
	if err != nil {
		t.Fatal(err)
	}
	if b := PayloadDigest(run2.Selected); a != b {
		t.Fatal("payload digest not deterministic")
	}
}
