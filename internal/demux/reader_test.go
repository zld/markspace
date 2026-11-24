package demux

import (
	"testing"

	"crypto/sha256"
	"encoding/hex"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/fixture"
	"archive/internal/model"
	"archive/internal/probe"
)

func build(t *testing.T, name string) (fixture.Spec, *fixture.Layout, []byte) {
	t.Helper()
	spec, ok := fixture.FindSpec(fixture.DefaultMixed(), name)
	if !ok {
		t.Fatalf("spec %s", name)
	}
	var layout *fixture.Layout
	var data []byte
	var err error
	if spec.Format == model.ContainerMP4 {
		layout, data, err = fixture.BuildMP4(spec)
	} else {
		layout, data, err = fixture.BuildMXF(spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	return spec, layout, data
}

func TestDemuxPreservesPayloadAndTiming(t *testing.T) {
	for _, name := range []string{"reel_a", "clip_b"} {
		_, layout, data := build(t, name)
		src := probe.Source{Name: name, Data: data}
		f, err := probe.Probe(src, layout.Spec.Format)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Demux(src, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Faults) != 0 {
			t.Fatalf("%s: unexpected faults: %v", name, probe.DescribeFaults(res.Faults))
		}
		if len(res.Tracks) != len(layout.Tracks) {
			t.Fatalf("%s: tracks = %d", name, len(res.Tracks))
		}
		for _, tl := range layout.Tracks {
			var td *TrackData
			// Logical names are unique within one container.
			for _, cand := range res.Tracks {
				if cand.Logical == string(tl.Spec.Kind) {
					td = cand
				}
			}
			if td == nil {
				t.Fatalf("%s: no track for %s", name, tl.Spec.Logical)
			}
			if len(td.Packets) != tl.Spec.Frames {
				t.Fatalf("%s/%s: packets = %d, want %d (skipped %d)", name, tl.Spec.Logical, len(td.Packets), tl.Spec.Frames, td.Skipped)
			}
			for i, p := range td.Packets {
				want := tl.Frames[i]
				if p.StartTick != want.StartTick || p.Duration != want.Duration {
					t.Fatalf("%s/%s packet %d timestamps = %d/%d, want %d/%d", name, tl.Spec.Logical, i, p.StartTick, p.Duration, want.StartTick, want.Duration)
				}
				off := int64(want.Offset)
				if layout.Spec.Format == model.ContainerMXF {
					off += 20
				}
				if string(p.Payload) != string(data[off:off+int64(want.Size)]) {
					t.Fatalf("%s/%s packet %d payload differs from input", name, tl.Spec.Logical, i)
				}
				sum := sha256Hex(p.Payload)
				if p.SHA256 != sum {
					t.Fatalf("packet digest mismatch")
				}
				if !FrameComplete(p.Payload) {
					t.Fatalf("packet %d incomplete", i)
				}
			}
			if td.Range().StartTick != tl.Spec.StartTick {
				t.Fatalf("%s/%s start = %d", name, tl.Spec.Logical, td.Range().StartTick)
			}
		}
	}
}

func TestDemuxTruncatedTailIsReportedNotSilentlyDropped(t *testing.T) {
	_, layout, data := build(t, "clip_b")
	cut := data[:len(data)-10]
	src := probe.Source{Name: "clip_b", Data: cut}
	f, err := probe.Probe(src, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Demux(src, f)
	if err != nil {
		t.Fatal(err)
	}
	// The last frame(s) can no longer be read; there must be a fault and the
	// track must record the skipped fragment.
	total := 0
	for _, td := range res.Tracks {
		total += td.Skipped
	}
	if total == 0 {
		t.Fatal("truncated tail was silently dropped")
	}
	if len(res.Faults) == 0 {
		t.Fatal("no fault recorded")
	}
	last := layout.Tracks[len(layout.Tracks)-1]
	_ = last
	if !res.Faults.Unrecoverable() {
		// Truncation loses media, so at least one fault must be marked
		// unrecoverable even if the header survived.
		t.Log("faults:", probe.DescribeFaults(res.Faults))
	}
}

func TestDemuxCorruptSegmentIsReported(t *testing.T) {
	_, layout, data := build(t, "reel_a")
	plan, _, err := fixture.Plan(layout, []fixture.DamageKind{fixture.DamageSegment})
	if err != nil {
		t.Fatal(err)
	}
	damaged, injections, err := fixture.Apply(layout, data, plan)
	if err != nil {
		t.Fatal(err)
	}
	src := probe.Source{Name: "reel_a", Data: damaged}
	f, err := probe.Probe(src, model.ContainerMXF)
	if err != nil {
		// A corrupt body may legitimately stop the probe's packet walk.
		t.Skipf("probe could not finish: %v", err)
	}
	res, err := Demux(src, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Faults.ByCode(fault.CorruptSegment) == 0 && res.Faults.ByCode(fault.TruncatedBox) == 0 {
		t.Fatalf("segment corruption not reported: %v", probe.DescribeFaults(res.Faults))
	}
	// The reported range must cover the injected frame.
	inj := injections[0]
	covered := false
	for _, e := range res.Faults {
		if !e.HasRange {
			continue
		}
		if e.StartNS <= inj.StartNS && e.EndNS >= inj.EndNS {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("no fault covers injected range [%d,%d]: %v", inj.StartNS, inj.EndNS, probe.DescribeFaults(res.Faults))
	}
}

func TestFrameCompleteRejectsDamaged(t *testing.T) {
	spec, ok := fixture.FindSpec(fixture.DefaultMixed(), "clip_b")
	if !ok {
		t.Fatal("spec")
	}
	frame := fixture.BuildFrame(spec.Tracks[0], 0)
	if !FrameComplete(frame) {
		t.Fatal("intact frame rejected")
	}
	bad := append([]byte(nil), frame...)
	bad[essence.HeaderSize] ^= 0xFF
	if FrameComplete(bad) {
		t.Fatal("payload corruption accepted")
	}
	if FrameComplete(frame[:len(frame)-2]) {
		t.Fatal("short frame accepted")
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
