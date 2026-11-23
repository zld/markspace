package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/model"
	"mediapack/internal/probe"
	"mediapack/internal/timeline"
)

func TestGenerateAndScanRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bundles, err := Generate(dir, DefaultSpecs(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 3 {
		t.Fatalf("bundles = %d", len(bundles))
	}
	lim := probe.DefaultLimits()
	for _, b := range bundles {
		meta, err := probe.Scan(b.Container, lim)
		if err != nil {
			t.Fatalf("scan %s: %v", b.Container, err)
		}
		if len(meta.Tracks) != 1 || meta.Tracks[0].TimeScale != b.Spec.ClockRate {
			t.Fatalf("bad meta for %s: %+v", b.Container, meta.Tracks)
		}
		raw, err := os.ReadFile(b.RTPSidecar)
		if err != nil {
			t.Fatal(err)
		}
		pkts, offs, err := timeline.ParseRTPPacketStream(raw)
		if err != nil {
			t.Fatalf("rtp parse: %v", err)
		}
		if len(pkts) < b.Spec.Seconds || len(offs) != len(pkts) {
			t.Fatalf("packet count wrong: %d", len(pkts))
		}
	}
}

func TestGeneratedStreamWrapsAndIsMonotonic(t *testing.T) {
	dir := t.TempDir()
	spec := TrackSpec{ID: 1, Kind: probe.KindVideo, ClockRate: 90000, Seconds: 6, WrapNear: true}
	bundles, err := Generate(dir, []TrackSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(bundles[0].RTPSidecar)
	pkts, _, err := timeline.ParseRTPPacketStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	ct := &timeline.ClockTable{TrackID: 1, ClockRate: 90000, Packets: pkts}
	var es model.Errors
	tl, err := ct.Expand(&es)
	if err != nil {
		t.Fatal(err)
	}
	if tl.WrapCount == 0 {
		t.Fatal("WrapNear fixture must exhibit at least one wrap")
	}
	for i := 1; i < len(tl.Events); i++ {
		if tl.Events[i].UnwrappedTS < tl.Events[i-1].UnwrappedTS {
			t.Fatalf("unwrapped clock not monotonic at %d", i)
		}
	}
}

func TestVFRKeyframesWholeSecondAligned(t *testing.T) {
	dir := t.TempDir()
	spec := TrackSpec{ID: 2, Kind: probe.KindVideo, ClockRate: 90000, Seconds: 4, WrapNear: false, VFR: true}
	bundles, _ := Generate(dir, []TrackSpec{spec})
	raw, _ := os.ReadFile(bundles[0].RTPSidecar)
	pkts, _, _ := timeline.ParseRTPPacketStream(raw)
	// First packet of each second is a keyframe on an exact tick.
	base := pkts[0].Timestamp
	for sec := 0; sec < 4; sec++ {
		want := base + uint32(sec)*90000
		found := false
		// Keyframes are at indices sec*5 (one keyframe + four VFR gaps).
		idx := sec * 5
		if idx < len(pkts) && pkts[idx].Timestamp == want {
			found = true
		}
		if !found {
			t.Fatalf("missing aligned keyframe at second %d (want ts %d)", sec, want)
		}
	}
}

func TestFaultInjectionOffsets(t *testing.T) {
	dir := t.TempDir()
	bundles, err := Generate(dir, DefaultSpecs(2))
	if err != nil {
		t.Fatal(err)
	}
	mp4 := bundles[0].Container
	mxf := bundles[1].Container

	trunc, err := InjectTruncatedBox(mp4, filepath.Join(dir, "trunc.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	_, serr := probe.Scan(trunc.Path, probe.DefaultLimits())
	pe, ok := model.AsPipeline(serr)
	if !ok || pe.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncated_box, got %v", serr)
	}
	if pe.ByteRange.Offset != trunc.Offset {
		t.Fatalf("reported offset %d != injected %d", pe.ByteRange.Offset, trunc.Offset)
	}

	badLen, _ := InjectIllegalLength(mp4, filepath.Join(dir, "badlen.mp4"))
	_, serr = probe.Scan(badLen.Path, probe.DefaultLimits())
	if pe, ok := model.AsPipeline(serr); !ok ||
		(pe.Code != model.CodeIllegalLength && pe.Code != model.CodeTruncatedBox) {
		t.Fatalf("want illegal/truncated, got %v", serr)
	}

	badMXF, _ := InjectTruncatedMXF(mxf, filepath.Join(dir, "trunc.mxf"))
	_, serr = probe.Scan(badMXF.Path, probe.DefaultLimits())
	if pe, ok := model.AsPipeline(serr); !ok || pe.Code != model.CodeTruncatedBox {
		t.Fatalf("want mxf truncated_box, got %v", serr)
	}
}

func TestChangedInputSummary(t *testing.T) {
	dir := t.TempDir()
	bundles, _ := Generate(dir, DefaultSpecs(1))
	f, err := InjectChangedInput(bundles[0].Container, filepath.Join(dir, "changed.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(bundles[0].Container)
	changed, _ := os.ReadFile(f.Path)
	if len(changed) != len(orig)+1 {
		t.Fatal("changed input must grow by exactly one byte")
	}
}
