package fixture

import (
	"testing"

	"mediad/media"
	"mediad/timeline"
)

func TestBuildDemoIsDeterministic(t *testing.T) {
	a := BuildDemo(DemoOptions{Seed: 7, Loss: 0.04})
	b := BuildDemo(DemoOptions{Seed: 7, Loss: 0.04})
	if len(a.Files) != len(b.Files) || len(a.Damaged) != len(b.Damaged) {
		t.Fatal("file counts differ between identical seeds")
	}
	for i := range a.Files {
		if string(a.Files[i].Bytes) != string(b.Files[i].Bytes) {
			t.Fatalf("file %s differs between identical seeds", a.Files[i].Name)
		}
	}
	// A different seed must produce different media bytes.
	c := BuildDemo(DemoOptions{Seed: 8, Loss: 0.04})
	if string(a.Files[0].Bytes) == string(c.Files[0].Bytes) {
		t.Fatal("different seeds produced identical media")
	}
}

func TestDemoClipsParse(t *testing.T) {
	d := BuildDemo(DemoOptions{Seed: 7, Loss: 0.04})
	for _, f := range d.Files {
		if f.IsRTP {
			continue
		}
		clip, err := media.Parse(f.Name, f.Bytes)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if len(clip.Tracks) != 3 {
			t.Fatalf("%s: tracks = %d", f.Name, len(clip.Tracks))
		}
		video, ok := clip.VideoTrack()
		if !ok || len(video.Samples) == 0 {
			t.Fatalf("%s: no video samples", f.Name)
		}
		if len(clip.ChplBoxes()) != 1 {
			t.Fatalf("%s: chpl boxes = %d", f.Name, len(clip.ChplBoxes()))
		}
	}
}

func TestDemoRTPRecoveryMeetsThreshold(t *testing.T) {
	d := BuildDemo(DemoOptions{Seed: 7, Loss: 0.04})
	var capture []byte
	for _, f := range d.Files {
		if f.IsRTP {
			capture = f.Bytes
		}
	}
	if capture == nil {
		t.Fatal("no RTP capture generated")
	}
	packets, err := media.ParseRTPCapture("stream.rtp", capture)
	if err != nil {
		t.Fatal(err)
	}
	rec := media.RecoverRTP(packets)
	if rec.LossRecoveryRate() < 0.95 {
		t.Fatalf("loss recovery = %.4f, want ≥ 0.95", rec.LossRecoveryRate())
	}
	if rec.Duplicates == 0 {
		t.Fatal("demo should include duplicates")
	}
	if rec.Reordered == 0 {
		t.Fatal("demo should include reorders")
	}
}

func TestDemoDamagedInputsAreRejected(t *testing.T) {
	d := BuildDemo(DemoOptions{Seed: 7, Loss: 0.04})
	for _, bad := range d.Damaged {
		clip, err := media.Parse(bad.Name, bad.Bytes)
		if err == nil {
			// The clip itself parses (the box is a top-level extra); the
			// metadata parser must reject the damaged box.
			_, issues := parseMetadataForTest(clip)
			if !issues.HasErrors() {
				t.Fatalf("%s: expected a metadata error for %s", bad.Name, bad.Reason)
			}
			continue
		}
		if issue, ok := timeline.IssueOf(err); !ok || issue.Message == "" {
			t.Fatalf("%s: error lacks a located issue: %v", bad.Name, err)
		}
	}
}

// parseMetadataForTest reimplements the minimal metadata scan so the fixture
// package can assert that a damaged box is reported without importing pipeline.
func parseMetadataForTest(clip *media.Clip) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	for _, ch := range clip.ChplBoxes() {
		evs, is := media.ParseChpl(ch.Payload, clip.Path, 0)
		events = append(events, evs...)
		issues.Merge(is)
	}
	for _, em := range clip.EmsgBoxes() {
		info, err := media.ParseEmsg(em.Payload, clip.Path, em.Offset)
		if err != nil {
			issues.AddError(timeline.CodeInvalidLength, err)
			continue
		}
		ev, err := media.EmsgToEvent(info, clip.Path, 0)
		if err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, issues
}
