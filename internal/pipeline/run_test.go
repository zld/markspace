package pipeline

import (
	"context"
	"errors"
	"testing"
)

func runFull(t *testing.T, dir string, interruptAfter string) (*Pipeline, error) {
	t.Helper()
	p, err := New(Config{Dir: dir, Seed: 7, InterruptAfter: interruptAfter, RegenerateInput: true})
	if err != nil {
		t.Fatal(err)
	}
	rerr := p.Run(context.Background())
	if interruptAfter == "" && rerr != nil {
		t.Fatal(rerr)
	}
	return p, rerr
}

func TestFullRunDeterministic(t *testing.T) {
	p1, _ := runFull(t, t.TempDir(), "")
	p2, _ := runFull(t, t.TempDir(), "")
	if p1.Res.Merge.ManifestSHA256 != p2.Res.Merge.ManifestSHA256 {
		t.Fatalf("manifest hash differs: %s vs %s",
			p1.Res.Merge.ManifestSHA256, p2.Res.Merge.ManifestSHA256)
	}
	if len(p1.Res.Media.FrameHashes) != 30 {
		t.Fatalf("frames = %d", len(p1.Res.Media.FrameHashes))
	}
	// Clean stream: no loss, no dups; lossy stream: loss > 0.
	clean := p1.Res.RTP.Streams[0]
	if clean.Lost != 0 || clean.Dups != 0 || clean.Unique != clean.Packets {
		t.Fatalf("clean stream = %+v", clean)
	}
	lossy := p1.Res.RTP.Streams[1]
	if lossy.Lost == 0 || lossy.Dups == 0 {
		t.Fatalf("lossy stream = %+v", lossy)
	}
	// Alignment error within one sample duration (1/90000 s in ns).
	if p1.Res.RTCP.AlignmentErrorNanos > 1_000_000_000/90000 {
		t.Fatalf("alignment error = %d ns", p1.Res.RTCP.AlignmentErrorNanos)
	}
}

func TestResumeAfterInterrupt(t *testing.T) {
	dir := t.TempDir()
	_, err := runFull(t, dir, "validate")
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	// Second run on the same dir must not recompute completed stages.
	p2, _ := runFull(t, dir, "")
	for _, s := range []string{"ingest", "rtp", "rtcp", "scan", "validate"} {
		if !contains(p2.LoadedStages, s) {
			t.Fatalf("stage %s not loaded from checkpoint; loaded=%v ran=%v",
				s, p2.LoadedStages, p2.RanStages)
		}
	}
	// Uninterrupted reference run.
	ref, _ := runFull(t, t.TempDir(), "")
	if p2.Res.Merge.ManifestSHA256 != ref.Res.Merge.ManifestSHA256 {
		t.Fatalf("resumed manifest %s != uninterrupted %s",
			p2.Res.Merge.ManifestSHA256, ref.Res.Merge.ManifestSHA256)
	}
}

func TestCorruptSampleOffsetStable(t *testing.T) {
	p, _ := runFull(t, t.TempDir(), "")
	var offset int64 = -1
	for _, d := range p.Res.Fragment.Files["corrupt-frag.mp4"].Diags {
		if d.Code == "corrupt-sample" {
			offset = d.Offset
		}
	}
	if offset < 0 {
		t.Fatal("no corrupt-sample diagnostic")
	}
	// Regenerate and confirm the same offset is reported.
	p2, _ := runFull(t, t.TempDir(), "")
	for _, d := range p2.Res.Fragment.Files["corrupt-frag.mp4"].Diags {
		if d.Code == "corrupt-sample" && d.Offset != offset {
			t.Fatalf("offset moved: %d -> %d", offset, d.Offset)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
