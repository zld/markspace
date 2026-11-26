package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rtpregress/internal/audio"
)

func TestTargetRateMirrorsAudio(t *testing.T) {
	if audioTargetRate != audio.TargetRate {
		t.Fatalf("manifest rate %d != audio.TargetRate %d",
			audioTargetRate, audio.TargetRate)
	}
}

func TestBuilderWritesDeterministicSortedJSON(t *testing.T) {
	dir := t.TempDir()
	build := func() string {
		b := NewBuilder(audio.TargetRate, audio.TargetChannels, audio.TargetBitrate)
		// insert rows out of order; sorting must canonicalize them
		b.AddSegment(SegmentEntry{Segment: 2, StartSample: 96, PCMSHA256: "z"})
		b.AddSegment(SegmentEntry{Segment: 0, StartSample: 0, PCMSHA256: "a"})
		b.AddSegment(SegmentEntry{Segment: 1, StartSample: 48, PCMSHA256: "m"})
		b.AddGap(GapEntry{FromNanos: 50, Code: "rtp_sequence_gap"})
		b.AddGap(GapEntry{FromNanos: 10, Code: "ts_continuity_counter_jump"})
		b.AddPSI(PSIEntry{Offset: 376, Table: "PMT", Version: 1})
		b.SetTimeline(144, 3_000_000, 250)
		b.SetHash("deadbeef")
		path, err := b.Write(dir, "manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		return string(data)
	}
	first := build()
	second := build()
	if first != second {
		t.Fatal("manifest JSON must be byte-identical across runs")
	}
	var rep Report
	if err := json.Unmarshal([]byte(first), &rep); err != nil {
		t.Fatalf("output must be valid JSON: %v", err)
	}
	if rep.Segments[0].Segment != 0 || rep.Segments[2].Segment != 2 {
		t.Fatalf("segments not sorted: %+v", rep.Segments)
	}
	if rep.Gaps[0].FromNanos != 10 {
		t.Fatalf("gaps not sorted: %+v", rep.Gaps)
	}
	if rep.Metrics.BitrateTarget != 0 {
		// metrics default zero when unset
	}
	if rep.ManifestHash != "deadbeef" || rep.DriftPPM != 250 {
		t.Fatalf("scalar fields lost: %+v", rep)
	}
}

// genFMp4 asks ffmpeg to mux a short AAC stream as fragmented MP4 with the
// canonical 48 kHz timescale, then validates it with go-mp4.
func TestValidateFMp4ReadsTiming(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.m4s")
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-ac", "1", "-c:a", "aac", "-b:a", "96k",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "-y", out,
	}
	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg fmp4: %v: %s", err, stderr.String())
	}
	timing, err := ValidateFMp4(out)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if timing.Timescale != 48000 {
		t.Fatalf("timescale = %d", timing.Timescale)
	}
	if len(timing.FragmentBase) == 0 || len(timing.FragmentSamples) == 0 {
		t.Fatalf("expected fragmented boxes, got tfdt=%d trun=%d",
			len(timing.FragmentBase), len(timing.FragmentSamples))
	}
	// first fragment must start at sample zero and be monotonic
	if timing.FragmentBase[0] != 0 {
		t.Fatalf("first tfdt = %d, want 0", timing.FragmentBase[0])
	}
}

func TestMaxTimelineError(t *testing.T) {
	tm := &FMp4Timing{
		Timescale:    48000,
		FragmentBase: []uint64{0, 48000, 96001}, // last is 1 sample late
	}
	// one sample at 48 kHz = 1/48 ms ~= 0.020833 ms
	got := tm.MaxTimelineErrorMS([]int64{0, 48000, 96000})
	if got < 0.020 || got > 0.021 {
		t.Fatalf("max error ms = %f, want ~0.0208", got)
	}
}
