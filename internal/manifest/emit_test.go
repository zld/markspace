package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

func emitInput(t *testing.T, start int64, segments [][2]int64) EmitInput {
	t.Helper()
	tl, err := clock.NewTimeline(clock.Rate48000)
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.SetStart(start); err != nil {
		t.Fatal(err)
	}
	for i, seg := range segments {
		if i%2 == 0 {
			if _, err := tl.AddMedia("t", seg[0], seg[1]); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := tl.AddGap(seg[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	return EmitInput{
		AssetID:           "asset-1",
		Rate:              clock.Rate48000,
		MovieTimescale:    48000,
		StartTick:         start,
		SampleRate:        48000,
		Layout:            "stereo",
		Timeline:          tl,
		RecoveredSamples:  22,
		MissingDurationMS: 100,
	}
}

func TestEmitCleanTimeline(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 48000}})
	doc, err := Emit(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Intervals) != 1 {
		t.Fatalf("contiguous media must merge: %+v", doc.Intervals)
	}
	if doc.Intervals[0].DurationMS != 1000 {
		t.Fatalf("duration %d", doc.Intervals[0].DurationMS)
	}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmitGapMarkedAndExcluded(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 4800}, {0, 48000}})
	doc, err := Emit(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Intervals) != 2 {
		t.Fatalf("intervals %+v", doc.Intervals)
	}
	if !doc.Intervals[1].MissingBefore {
		t.Fatal("second interval must mark the gap")
	}
	if doc.Intervals[1].StartMS != 1100 {
		t.Fatalf("second start %d, gap must not be compressed", doc.Intervals[1].StartMS)
	}
}

func TestEmitNonZeroStart(t *testing.T) {
	in := emitInput(t, 96000, [][2]int64{{0, 48000}})
	doc, err := Emit(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.StartTick != 96000 {
		t.Fatalf("start tick %d", doc.StartTick)
	}
	if doc.Intervals[0].StartMS != 2000 {
		t.Fatalf("interval start %d, want 2000", doc.Intervals[0].StartMS)
	}
}

func TestEmitHashDeterministic(t *testing.T) {
	in1 := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 48000}})
	d1, _ := Emit(in1)
	in2 := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 48000}})
	d2, _ := Emit(in2)
	if d1.OutputSHA256 != d2.OutputSHA256 {
		t.Fatalf("hash not deterministic: %s vs %s", d1.OutputSHA256, d2.OutputSHA256)
	}
}

func TestEmitHashCoversContent(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}})
	doc, _ := Emit(in)
	docCopy := *doc
	docCopy.OutputSHA256 = ""
	b, _ := docCopy.Canonical()
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != doc.OutputSHA256 {
		t.Fatal("hash does not cover the document")
	}
}

func TestEmitValidationRejectsBadRate(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}})
	in.SampleRate = 44100
	_, err := Emit(in)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !errors.Is(err, fault.OfClass(fault.ClassAudio, "sample-rate")) {
		t.Fatalf("got %v", err)
	}
}

func TestEmitNoTimeline(t *testing.T) {
	_, err := Emit(EmitInput{Rate: clock.Rate48000})
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "no-timeline")) {
		t.Fatalf("got %v", err)
	}
}

func TestEmitIntervalsDoNotOverlap(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 4800}, {0, 48000}, {0, 2400}, {0, 48000}})
	doc, err := Emit(in)
	if err != nil {
		t.Fatal(err)
	}
	prev := int64(-1)
	for _, iv := range doc.Intervals {
		if prev >= 0 && iv.StartMS < prev {
			t.Fatalf("overlap at %+v", iv)
		}
		prev = iv.StartMS + iv.DurationMS
	}
}

func TestEmitAlignmentBound(t *testing.T) {
	in := emitInput(t, 96000, [][2]int64{{0, 4410}, {0, 480}, {0, 4410}})
	doc, err := Emit(in)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ManifestAlignmentMS > 1 {
		t.Fatalf("alignment %d", doc.ManifestAlignmentMS)
	}
}

func TestDocumentJSONRoundTrip(t *testing.T) {
	in := emitInput(t, 0, [][2]int64{{0, 48000}, {0, 4800}, {0, 48000}})
	in.TrackStats = []TrackStats{{TrackID: 1, InputRate: 44100, OutputRate: 48000, OutputLayout: "stereo"}}
	in.PacketStats = PacketStats{Packets: 100, ActualKbps: 1536}
	doc, _ := Emit(in)
	b, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var back Document
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.OutputSHA256 != doc.OutputSHA256 {
		t.Fatal("hash lost in round trip")
	}
	if !strings.Contains(string(b), `"actual_kbps": 1536`) {
		t.Fatalf("packet stats missing:\n%s", b)
	}
}
