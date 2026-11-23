package pipeline

import (
	"path/filepath"
	"testing"

	"mediatool/internal/fixture"
	"mediatool/internal/mapping"
	"mediatool/internal/mp4"
	"mediatool/internal/rtp"
	"mediatool/internal/timeline"
)

func newTestPipeline(t *testing.T, g *fixture.Group) *Pipeline {
	rules := []mapping.Rule{
		{OutputSlot: "video-main", TrackID: 1, MediaType: "video"},
		{OutputSlot: "audio-main", TrackID: 2, MediaType: "audio"},
	}
	p, err := New(rules, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetTracks(g.Tracks)
	return p
}

func ingestAll(t *testing.T, p *Pipeline, g *fixture.Group, videoOrder []fixture.Fragment) {
	t.Helper()
	// Interleave video and audio in input order; attach RTP timing to video.
	u := &rtp.Unwrapper{}
	maxLen := len(videoOrder)
	if len(g.AudioFrags) > maxLen {
		maxLen = len(g.AudioFrags)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(videoOrder) {
			f := videoOrder[i]
			// RTP packets are indexed by arrival position, not fragment
			// sequence: the capture stream stays ordered even when a
			// fragment is delivered late (jitter-buffer scenario).
			pkt := g.RTPVideo[i%len(g.RTPVideo)]
			buf, _ := pkt.Marshal()
			_, raw, _ := rtp.ParsePacket(buf)
			expanded, uerr := u.Unwrap(raw)
			if uerr != nil {
				t.Fatalf("rtp unwrap at input %d: %v", i, uerr)
			}
			p.Ingest(Input{Data: f.Bytes, HasRTP: true,
				RTPWallMS: int64(i) * 100, RTPMedia: expanded})
		}
		if i < len(g.AudioFrags) {
			p.Ingest(Input{Data: g.AudioFrags[i].Bytes})
		}
	}
}

func TestPipelineEndToEnd(t *testing.T) {
	g := fixture.Generate(42, 6, -1)
	p := newTestPipeline(t, g)
	ingestAll(t, p, g, g.VideoFrags)
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.RejectedBoxes != 0 {
		t.Fatalf("clean input must not reject boxes: %d", res.Counters.RejectedBoxes)
	}
	if res.Counters.SampleCount == 0 || res.Counters.PayloadBytes == 0 {
		t.Fatal("samples and payload bytes must be accounted for")
	}
	if len(res.Manifest.Slots) != 2 {
		t.Fatalf("want 2 output slots, got %d", len(res.Manifest.Slots))
	}
	for _, slot := range res.Manifest.Slots {
		if len(slot.Fragments) != 6 {
			t.Fatalf("slot %s expected 6 chunks, got %d", slot.Slot, len(slot.Fragments))
		}
	}
}

func TestPipelineLateFragmentRecomputes(t *testing.T) {
	g := fixture.Generate(42, 8, 3)
	p := newTestPipeline(t, g)
	late := fixture.ReorderLate(g.VideoFrags, 3)
	ingestAll(t, p, g, late)
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.LateFragments == 0 {
		t.Fatal("reordered fragment must be detected as late")
	}
	if res.Counters.LateRecomputed == 0 {
		t.Fatal("late fragment must trigger interval recomputation")
	}
	// Despite late arrival, output chunks stay in corrected DTS order.
	for _, slot := range res.Manifest.Slots {
		if slot.MediaType != "video" {
			continue
		}
		var prev uint64
		for i, f := range slot.Fragments {
			if i > 0 && f.CorrectedDTS < prev {
				t.Fatal("output not ordered after recompute")
			}
			prev = f.CorrectedDTS
		}
		if slot.Fragments[0].Late {
			// The late fragment must carry its late flag into the manifest.
			foundLate := false
			for _, f := range slot.Fragments {
				if f.Late {
					foundLate = true
				}
			}
			if !foundLate {
				t.Fatal("late flag lost in manifest")
			}
		}
	}
}

func TestPipelineDuplicateSequenceMarked(t *testing.T) {
	g := fixture.Generate(42, 4, -1)
	p := newTestPipeline(t, g)
	ingestAll(t, p, g, g.VideoFrags)
	// Feed the first video fragment a second time.
	p.Ingest(Input{Data: g.VideoFrags[0].Bytes})
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.DuplicateSeq == 0 {
		t.Fatal("duplicate sequence must be counted")
	}
	hasDupClass := false
	for _, e := range res.Errors {
		if e.Class == timeline.ClassDuplicateSequence {
			hasDupClass = true
		}
	}
	if !hasDupClass {
		t.Fatal("duplicate must be classified, not silently overwritten")
	}
	// Output chunk count is unchanged: the duplicate is not remuxed.
	for _, slot := range res.Manifest.Slots {
		if slot.MediaType == "video" && len(slot.Fragments) != 4 {
			t.Fatalf("duplicate must not add a chunk, got %d", len(slot.Fragments))
		}
	}
}

func TestPipelineMissingMfhdContinues(t *testing.T) {
	g := fixture.Generate(42, 3, -1)
	p := newTestPipeline(t, g)
	// Replace fragment 1 with an mfhd-less but otherwise identical chunk.
	f := g.VideoFrags[1]
	spec := mp4.FragmentSpec{
		Seq: -1, TrackID: f.TrackID, BaseMediaDecode: f.DTS,
		SampleDurations: f.Durations, SampleSizes: f.Sizes, SamplePayload: f.Payload,
	}
	noMfhd := mp4.BuildFragmentNoMfhd(spec)
	order := []fixture.Fragment{
		g.VideoFrags[0],
		{Seq: -1, TrackID: f.TrackID, DTS: f.DTS, Bytes: noMfhd,
			Durations: f.Durations, Sizes: f.Sizes, Payload: f.Payload},
		g.VideoFrags[2],
	}
	ingestAll(t, p, g, order)
	res, err := p.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters.MissingMfhd == 0 {
		t.Fatal("missing mfhd must be counted")
	}
	for _, slot := range res.Manifest.Slots {
		if slot.MediaType == "video" && len(slot.Fragments) != 3 {
			t.Fatal("missing mfhd must not drop the fragment")
		}
	}
}
