package pipeline

import (
	"path/filepath"
	"testing"

	"mediavault/internal/fixture"
	"mediavault/internal/ingest"
	"mediavault/internal/timeline"
)

// TestRTPWrapMonotonicEndToEnd proves the RTP side-channel's 32-bit counter
// wrap is unwrapped transparently: no boundary-jump error, strictly
// monotonic DTS across the wrap, and a recorded wrap count.
func TestRTPWrapMonotonicEndToEnd(t *testing.T) {
	dir := t.TempDir()
	spec := rtpSpec()
	path := filepath.Join(dir, "s.rtp")
	if err := fixture.WriteRTP(path, spec); err != nil {
		t.Fatal(err)
	}
	capInfo := ingest.RTPCapture{
		Source: "rtp", ClockRate: spec.ClockRate, Kind: timeline.KindVideo,
		JumpThreshold: 1 << 33,
	}
	refs, err := ingest.ReadRTPFile(path, capInfo)
	if err != nil {
		t.Fatal(err)
	}
	samples, errs, err := ingest.IngestRTP(refs, capInfo)
	if err != nil || len(errs) != 0 {
		t.Fatalf("wrap must be transparent: err=%v errs=%v", err, errs)
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].TS.DTS.Cmp(samples[i-1].TS.DTS) <= 0 {
			t.Fatalf("sample %d DTS not monotonic across wrap", i)
		}
	}
	if samples[len(samples)-1].TS.WrapCount == 0 {
		t.Fatal("wrap count must be recorded")
	}
}
