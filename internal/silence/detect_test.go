package silence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediaregress/internal/fixture"
	"mediaregress/internal/media"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// writeAudioFile builds an MP4 whose audio payloads are RTP packets with
// controllable amplitude and sequence, then returns its path and timeline.
func writeAudioFile(t *testing.T, amp func(i int) int16, packets int) (string, *timeline.Timeline, []split.Segment) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.mp4")
	spec := fixture.AudioSpecForTest(packets, amp)
	if err := fixture.WriteAudioMP4(path, spec); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tl, err := timeline.Build(in)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	segs, err := split.Tracks(tl, media.Second)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return path, tl, segs
}

func TestDetectSilenceRange(t *testing.T) {
	// 100 ms packets: 0-1.5 s loud, 1.5-2.5 s silent, loud after.
	path, tl, segs := writeAudioFile(t, func(i int) int16 {
		ms := int64(i) * 100
		if ms >= 1500 && ms < 2500 {
			return 0
		}
		return 2000
	}, 235)
	results, errs, err := Detect(path, tl, segs, 100*media.Millisecond, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d", len(results))
	}
	r := results[0]
	if len(r.Silent) != 1 {
		t.Fatalf("silent ranges=%+v", r.Silent)
	}
	got := r.Silent[0]
	// Boundaries are absolute 100ms multiples; the silent span is 1.5-2.5 s.
	if got.Window.Start != 1500*media.Millisecond || got.Window.End != 2500*media.Millisecond {
		t.Errorf("silent window=%+v", got.Window)
	}
	if got.Peak != 0 {
		t.Errorf("peak=%d", got.Peak)
	}
	if int(got.Threshold) != 10 {
		t.Errorf("threshold=%d", got.Threshold)
	}
	if got.Stage != media.StageSilence {
		t.Errorf("stage=%s", got.Stage)
	}
}

func TestWindowsCrossSegmentBoundary(t *testing.T) {
	// 0.8-1.2 s silent with 150 ms windows: the 0.9-1.05 s window spans the
	// 1 s segment cut and must not be clipped to either segment.
	path, tl, segs := writeAudioFile(t, func(i int) int16 {
		ms := int64(i) * 100
		if ms >= 800 && ms < 1200 {
			return 0
		}
		return 1000
	}, 235)
	results, _, err := Detect(path, tl, segs, 150*media.Millisecond, 5)
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	// Absolute 150 ms windows: the 900-1050 ms window straddles the 1 s cut
	// and must stay 150 ms long rather than being clipped to either segment.
	var straddle *WindowResult
	for i := range r.Windows {
		w := &r.Windows[i]
		if w.Window.Start == 900*media.Millisecond {
			straddle = w
		}
	}
	if straddle == nil {
		t.Fatalf("900 ms window missing: %+v", r.Windows)
	}
	if straddle.Window.End != 1050*media.Millisecond || straddle.Window.Duration() != 150*media.Millisecond {
		t.Errorf("straddling window=%+v", straddle.Window)
	}
	if len(straddle.Segments) != 2 {
		t.Errorf("straddling window segments=%v", straddle.Segments)
	}
	if !straddle.Silent {
		t.Errorf("window should be silent: %+v", straddle)
	}
	// The merged range also names both segments it touches.
	var straddleRange *Range
	for i := range r.Silent {
		if len(r.Silent[i].Segments) > 1 {
			straddleRange = &r.Silent[i]
		}
	}
	if straddleRange == nil {
		t.Fatalf("no silent range names two segments: %+v", r.Silent)
	}
}

func TestTruncatedAudioStopsTrack(t *testing.T) {
	path, tl, segs := writeAudioFile(t, func(i int) int16 { return 0 }, 235)
	// Cut the file in the middle of the audio data.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, fi.Size()-2000); err != nil {
		t.Fatal(err)
	}
	// Re-parse so the sample table still points at the original offsets, while
	// the file is shorter than the last samples require.
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	tl2, _ := timeline.Build(in)
	if tl2 == nil {
		t.Fatal("timeline nil")
	}
	results, errs, err := Detect(path, tl2, segs, 100*media.Millisecond, 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = tl
	if len(errs) == 0 {
		t.Fatal("truncation not reported")
	}
	r := results[0]
	if !r.Truncated {
		t.Fatal("result not marked truncated")
	}
	if r.TruncatedFrom == 0 || r.TruncatedTo <= r.TruncatedFrom {
		t.Errorf("affected interval=%d..%d", r.TruncatedFrom, r.TruncatedTo)
	}
	first := errs[0]
	if first.Class != media.ClassSilenceTruncated || first.Track != r.Track {
		t.Errorf("error=%+v", first)
	}
	// No windows may be reported past the truncation point, and none at all
	// may describe bytes that were never read.
	for _, w := range r.Windows {
		if w.Window.End > r.TruncatedTo {
			t.Errorf("window %+v beyond truncation", w.Window)
		}
	}
	for _, sr := range r.Silent {
		if sr.Window.End > r.TruncatedTo {
			t.Errorf("silent range %+v beyond truncation", sr.Window)
		}
	}
}

func TestRTPSequenceGapReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gap.mp4")
	pkts := make([][]byte, 10)
	for i := range pkts {
		p := rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: 111, SequenceNumber: uint16(i), Timestamp: uint32(i * 1024),
		}}
		p.Payload = make([]byte, 64)
		if i == 5 {
			p.SequenceNumber = 99 // gap
		}
		b, _ := p.Marshal()
		pkts[i] = b
	}
	// Replace the audio sample payloads using the fixture helper.
	if err := fixture.WriteAudioPackets(path, 48000, 1024, pkts); err != nil {
		t.Fatal(err)
	}
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	results, errs, err := Detect(path, tl, nil, 100*media.Millisecond, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Fatal("sequence gap not reported")
	}
	if results[0].Track != "a0" {
		t.Errorf("track=%s", results[0].Track)
	}
	found := false
	for _, e := range errs {
		if e.Class == media.ClassCorruptInput && e.Track == "a0" && e.Offset > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("no positioned sequence error: %v", errs)
	}
}

func TestThresholdBoundary(t *testing.T) {
	// Peak exactly at the threshold counts as silent (<=).
	path, tl, segs := writeAudioFile(t, func(i int) int16 { return 100 }, 48)
	results, _, err := Detect(path, tl, segs, media.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(results[0].Silent) != 1 {
		t.Fatalf("silent=%+v", results[0].Silent)
	}
	results, _, err = Detect(path, tl, segs, media.Second, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(results[0].Silent) != 0 {
		t.Fatalf("peak 100 above threshold 99 must be loud: %+v", results[0].Silent)
	}
}

func TestUnreadableSamplesProduceNoSilence(t *testing.T) {
	// A track whose chunk table is unreadable has samples with no offsets.
	// Detection must not invent silence for bytes nobody read.
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.mp4")
	if err := fixture.WriteAudioMP4(path, fixture.AudioSpecForTest(50, func(int) int16 { return 0 })); err != nil {
		t.Fatal(err)
	}
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	tt := tl.Tracks[0]
	for i := range tt.Samples {
		tt.Samples[i].Offset = -1
	}
	res, errs, err := Detect(path, tl, nil, 100*media.Millisecond, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Fatal("unreadable samples not reported")
	}
	if len(res[0].Silent) != 0 {
		t.Fatalf("silence invented from unread bytes: %+v", res[0].Silent)
	}
	if len(res[0].Windows) != 0 {
		t.Fatalf("windows invented from unread bytes: %+v", res[0].Windows)
	}
}

func TestNoAudioTracks(t *testing.T) {
	tl := &timeline.Timeline{Tracks: []*timeline.TrackTimeline{
		{TrackID: "v0", Kind: track.KindVideo},
	}}
	results, _, err := Detect("/nonexistent", tl, nil, media.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results=%+v", results)
	}
}

func TestPeakAmplitudeSigned(t *testing.T) {
	// -32768 must have the same magnitude as 32768 would, without overflow.
	buf := []byte{0x00, 0x80, 0x01, 0x00}
	if got := peakAmplitude(buf); got != 32768 {
		t.Fatalf("peak=%d want 32768", got)
	}
	if got := peakAmplitude([]byte{1, 0, 2, 0}); got != 2 {
		t.Fatalf("peak=%d", got)
	}
}
