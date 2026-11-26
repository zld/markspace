package segment

import (
	"testing"

	"replaykit/media/clock"
	gomp4mp4 "replaykit/media/mp4"
)

// buildLongClip writes nFrag fragments of samplesPerFrag frames each; the
// first frame of every fragment is a keyframe.
func buildLongClip(t *testing.T, ts, frame uint32, nFrag, perFrag int) []byte {
	t.Helper()
	b := gomp4mp4.NewBuilder(ts, 1, frame)
	b.WriteInit()
	for f := 0; f < nFrag; f++ {
		specs := make([]gomp4mp4.SampleSpec, perFrag)
		base := uint64(f) * uint64(perFrag) * uint64(frame)
		for i := range specs {
			specs[i] = gomp4mp4.SampleSpec{
				Duration: frame,
				Key:      i == 0,
				Payload:  []byte{byte('A' + f%26), byte('a' + i), 0, 1, 2, 3},
			}
		}
		b.AddFragment(base, uint32(f+1), specs)
	}
	return b.Bytes()
}

func indexLayer(t *testing.T, name string, data []byte, ts uint32, frame int64) *Layer {
	t.Helper()
	limits := gomp4mp4.DefaultLimits()
	scan, err := gomp4mp4.ScanBytes(data, limits)
	if err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	idx, err := gomp4mp4.BuildIndex(data, scan, limits, nil)
	if err != nil {
		t.Fatalf("index %s: %v", name, err)
	}
	c, err := clock.New(ts, frame)
	if err != nil {
		t.Fatal(err)
	}
	return &Layer{Name: name, Data: data, Scan: scan, Index: idx, Clock: c, InputDigest: "digest-" + name}
}

func TestPlanWindowCutsSelfContainedFragmentRange(t *testing.T) {
	const ts, frame uint32 = 90000, 3000
	data := buildLongClip(t, ts, frame, 40, 5) // 40 frags * 15000 ticks = 600000 (6.66s)
	l := indexLayer(t, "720p", data, ts, int64(frame))

	// Event at 2.0s (tick 180000 = keyframe frag 12), +-0.5s.
	ev := Event{ID: "e1", AtNS: 2_000_000_000, PreNS: 500_000_000, PostNS: 500_000_000}
	w, err := PlanWindow(ev, l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !w.Recoverable {
		t.Fatalf("expected recoverable window: %+v anomalies=%+v", w, w.Anomalies)
	}
	if !w.LookupHit || !w.HasKeyframe {
		t.Fatal("expected keyframe lookup hit")
	}
	if w.Keyframe.DecodeTime != 135000 {
		t.Fatalf("keyframe=%d want 135000 (frag9)", w.Keyframe.DecodeTime)
	}
	if len(w.Ranges) < 4 {
		t.Fatalf("ranges=%d want >=4 fragments", len(w.Ranges))
	}
	if len(w.Bytes) == 0 || w.Digest == "" || len(w.Digest) != 64 {
		t.Fatal("window bytes/digest missing")
	}

	// The extracted clip must re-scan and re-index cleanly on its own.
	scan2, err := gomp4mp4.ScanBytes(w.Bytes, gomp4mp4.DefaultLimits())
	if err != nil {
		t.Fatalf("cut clip does not scan: %v", err)
	}
	idx2, err := gomp4mp4.BuildIndex(w.Bytes, scan2, gomp4mp4.DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("cut clip does not index: %v", err)
	}
	if len(idx2.Track.Samples) != w.SampleCount {
		t.Fatalf("re-indexed samples=%d want %d", len(idx2.Track.Samples), w.SampleCount)
	}
	if len(idx2.Keyframes) == 0 {
		t.Fatal("cut clip has no keyframes")
	}
}

func TestFanoutCrossLayerSkewZeroForEquivalentTimeline(t *testing.T) {
	hi := buildLongClip(t, 90000, 3000, 40, 5)
	lo := buildLongClip(t, 45000, 1500, 40, 5)
	layers := []*Layer{
		indexLayer(t, "720p", hi, 90000, 3000),
		indexLayer(t, "360p", lo, 45000, 1500),
	}
	events := []Event{{ID: "e1", AtNS: 2_000_000_000, PreNS: 500_000_000, PostNS: 500_000_000}}
	res := Fanout(events, layers, Options{})
	if res.Unrecoverable != 0 {
		t.Fatalf("unrecoverable=%d", res.Unrecoverable)
	}
	if res.Segments != 2 {
		t.Fatalf("segments=%d want 2", res.Segments)
	}
	if res.MaxSkewTicks != 0 {
		t.Fatalf("max skew=%d want 0 for equivalent timelines", res.MaxSkewTicks)
	}
	for _, b := range res.Boundaries {
		if !b.WithinFrame {
			t.Fatalf("boundary %+v should be within one frame", b)
		}
	}
}

func TestRoundtripNSExactForEventTimes(t *testing.T) {
	c, err := clock.New(90000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []int64{0, 1, 123456789, 2_000_000_000, 9_876_543_210} {
		back, rem, exact, err := RoundtripNS(c, ns)
		if err != nil || !exact || back != ns || rem != 0 {
			t.Fatalf("roundtrip %d -> back=%d rem=%d exact=%v err=%v", ns, back, rem, exact, err)
		}
	}
}
