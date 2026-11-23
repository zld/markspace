package probe

import (
	"testing"

	"mediacheck/internal/dash"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

func inspectProgram(t *testing.T, faults []model.FaultKind) (*dash.Manifest, *Report) {
	t.Helper()
	prog := fixture.Generate(17, faults)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	manifest, err := dash.ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return manifest, Inspect(manifest, prog)
}

func TestInspectHealthy(t *testing.T) {
	_, rep := inspectProgram(t, nil)
	if len(rep.Damaged) != 0 {
		t.Fatalf("healthy program has damaged ranges: %d", len(rep.Damaged))
	}
}

func TestInspectTruncationIsolated(t *testing.T) {
	manifest, rep := inspectProgram(t, []model.FaultKind{model.FaultTruncate})
	if len(rep.Damaged) < 2 {
		t.Fatalf("want >=2 damaged ranges, got %d", len(rep.Damaged))
	}
	// Damage must not spread: every damaged range maps to a real segment and
	// no healthy segment is flagged.
	damagedSet := map[string]bool{}
	for _, sr := range rep.Segments {
		if sr.Damaged {
			damagedSet[sr.Representation+"/"+itoa(sr.Sequence)] = true
		}
	}
	for _, rec := range manifest.Representations {
		for _, seg := range rec.Segments {
			key := rec.ID + "/" + itoa(seg.Sequence)
			want := seg.Truncated
			if damagedSet[key] != want && !seg.Late && !seg.Duplicate {
				t.Fatalf("segment %s damaged=%v want %v (fault spread?)",
					key, damagedSet[key], want)
			}
		}
	}
}

func TestInspectLateAndDuplicate(t *testing.T) {
	_, rep := inspectProgram(t, []model.FaultKind{
		model.FaultLate, model.FaultDuplicate,
	})
	kinds := map[model.FaultKind]bool{}
	for _, is := range rep.Issues {
		kinds[is.Kind] = true
	}
	if !kinds[model.FaultLate] || !kinds[model.FaultDuplicate] {
		t.Fatalf("missing late/duplicate issues: %v", kinds)
	}
}

func TestInspectKeyframeGap(t *testing.T) {
	// Remove leading key samples across consecutive segments so the cadence
	// gap from the last surviving key frame exceeds the 1.2 s ceiling.
	prog := fixture.Generate(17, nil)
	for _, idx := range []int{2, 3, 4} {
		prog.Video[0].Segments[idx].Samples[0].IsKeyframe = false
	}
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	manifest, err := dash.ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rep := Inspect(manifest, prog)
	found := false
	for _, is := range rep.Issues {
		if is.Kind == model.FaultKeyframe && is.Representation == "v-high" {
			found = true
		}
	}
	if !found {
		t.Fatal("keyframe gap not detected")
	}
}

func TestInspectPacketLoss(t *testing.T) {
	prog := fixture.Generate(17, []model.FaultKind{model.FaultTruncate})
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	manifest, err := dash.ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rep := Inspect(manifest, prog)
	var loss int
	for _, sr := range rep.Segments {
		loss += sr.PacketLoss
	}
	if loss == 0 {
		t.Fatal("truncated segment should show RTP packet loss")
	}
}

func TestCrossRepresentationAligned(t *testing.T) {
	_, rep := inspectProgram(t, nil)
	for _, is := range rep.Issues {
		if is.Kind == model.FaultClockSkew {
			t.Fatalf("healthy ladder should align, got skew: %+v", is)
		}
	}
}

func itoa(u uint32) string {
	if u == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
