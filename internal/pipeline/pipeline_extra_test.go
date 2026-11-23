package pipeline

import (
	"path/filepath"
	"testing"

	"mediatool/internal/fixture"
	"mediatool/internal/mapping"
	"mediatool/internal/mp4"
)

func TestPipelineRejectsCorruptBoxes(t *testing.T) {
	g := fixture.Generate(11, 3, -1)
	p := newTestPipeline(t, g)
	for _, f := range g.VideoFrags {
		p.Ingest(Input{Data: f.Bytes})
	}
	for _, c := range g.Corrupted {
		p.Ingest(Input{Data: c.Bytes})
	}
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.RejectedBoxes != len(g.Corrupted) {
		t.Fatalf("rejected = %d, want %d",
			res.Counters.RejectedBoxes, len(g.Corrupted))
	}
	// Valid fragments still produced output.
	for _, slot := range res.Manifest.Slots {
		if slot.MediaType == "video" && len(slot.Fragments) != 3 {
			t.Fatalf("valid chunks must survive: %d", len(slot.Fragments))
		}
	}
}

func TestPipelineTfdtWraparound32(t *testing.T) {
	g := fixture.Generate(11, 2, -1)
	// Hand-build two v0 (32-bit) tfdt fragments that cross 2^32.
	base0 := uint32(0xFFFFFFF0)
	f0 := g.VideoFrags[0]
	f1 := g.VideoFrags[1]
	spec0 := mp4.FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: uint64(base0), TfdtV0: true,
		SampleDurations: f0.Durations, SampleSizes: f0.Sizes, SamplePayload: f0.Payload,
	}
	spec1 := mp4.FragmentSpec{
		Seq: 1, TrackID: 1, BaseMediaDecode: uint64(base0) + 40, TfdtV0: true,
		SampleDurations: f1.Durations, SampleSizes: f1.Sizes, SamplePayload: f1.Payload,
	}
	p := newTestPipeline(t, g)
	p.Ingest(Input{Data: mp4.BuildFragment(spec0)})
	p.Ingest(Input{Data: mp4.BuildFragment(spec1)})
	for _, f := range g.AudioFrags {
		p.Ingest(Input{Data: f.Bytes})
	}
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.Wraparounds == 0 {
		t.Fatal("32-bit tfdt wrap must be detected as wraparound")
	}
	if res.Counters.Restarts != 0 {
		t.Fatal("legal wrap must not be classified as restart")
	}
	// Corrected DTS must stay monotonic across the boundary.
	for _, slot := range res.Manifest.Slots {
		if slot.MediaType != "video" {
			continue
		}
		if len(slot.Fragments) < 2 ||
			slot.Fragments[1].CorrectedDTS <= slot.Fragments[0].CorrectedDTS {
			t.Fatalf("corrected DTS regressed across wrap: %+v", slot.Fragments)
		}
	}
}

func TestPipelineTfdtRestart64(t *testing.T) {
	g := fixture.Generate(11, 2, -1)
	f0 := g.VideoFrags[0]
	f1 := g.VideoFrags[1]
	spec0 := mp4.FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 5_000_000_000,
		SampleDurations: f0.Durations, SampleSizes: f0.Sizes, SamplePayload: f0.Payload,
	}
	// v1 tfdt moving far backwards is a restart, not a wrap.
	spec1 := mp4.FragmentSpec{
		Seq: 1, TrackID: 1, BaseMediaDecode: 1000,
		SampleDurations: f1.Durations, SampleSizes: f1.Sizes, SamplePayload: f1.Payload,
	}
	p := newTestPipeline(t, g)
	p.Ingest(Input{Data: mp4.BuildFragment(spec0)})
	p.Ingest(Input{Data: mp4.BuildFragment(spec1)})
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.Restarts == 0 {
		t.Fatal("64-bit backward tfdt must be a restart")
	}
	if res.Counters.Wraparounds != 0 {
		t.Fatal("64-bit restart must not be mislabeled wraparound")
	}
}

func TestPipelineDeterministicSummary(t *testing.T) {
	build := func(dir string) string {
		g := fixture.Generate(42, 5, 2)
		rules := []mapping.Rule{{OutputSlot: "video-main", TrackID: 1, MediaType: "video"}}
		p, err := New(rules, filepath.Join(dir, "out"))
		if err != nil {
			t.Fatal(err)
		}
		p.SetTracks(g.Tracks)
		ordered := fixture.ReorderLate(g.VideoFrags, 2)
		for _, f := range ordered {
			p.Ingest(Input{Data: f.Bytes})
		}
		res, err := p.Finalize()
		if err != nil {
			t.Fatal(err)
		}
		return res.Manifest.SummaryHash
	}
	h1 := build(t.TempDir())
	h2 := build(t.TempDir())
	if h1 == "" || h1 != h2 {
		t.Fatalf("summary hashes must match across runs: %q vs %q", h1, h2)
	}
}

func TestPipelineUnknownTrackDiagnosed(t *testing.T) {
	g := fixture.Generate(11, 1, -1)
	p := newTestPipeline(t, g)
	// Fragment referencing a track id absent from the init data.
	fr := g.VideoFrags[0]
	spec := mp4.FragmentSpec{
		Seq: 0, TrackID: 99, BaseMediaDecode: 0,
		SampleDurations: fr.Durations, SampleSizes: fr.Sizes, SamplePayload: fr.Payload,
	}
	p.Ingest(Input{Data: mp4.BuildFragment(spec)})
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Errors {
		if e.Class == "mapping_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("unknown track id must produce a mapping conflict")
	}
}
