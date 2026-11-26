package recover_test

import (
	"os"
	"testing"

	"mediapack/internal/clock"
	merr "mediapack/internal/errors"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/recover"
	"mediapack/internal/sample"
)

func demoManifest(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
	if _, err := recover.Demo(recover.Options{WorkDir: dir, Rates: 2}); err != nil {
		t.Fatalf("demo: %v", err)
	}
	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func issuesByCat(m *manifest.Manifest, cat merr.Category) []merr.Issue {
	var out []merr.Issue
	for _, is := range m.Issues {
		if is.Category == cat {
			out = append(out, is)
		}
	}
	return out
}

// TestCCJumpPropagatesToKeyframeAndPack proves parse -> keyframe index ->
// packaged fragment association for a continuity-counter jump.
func TestCCJumpPropagatesToKeyframeAndPack(t *testing.T) {
	dir := t.TempDir()
	m := demoManifest(t, dir)
	L := sample.Generate(sample.DefaultConfig())

	jumps := issuesByCat(m, merr.CatCCJump)
	if len(jumps) != 1 {
		t.Fatalf("cc_jump=%d want 1", len(jumps))
	}
	j := jumps[0]
	if j.Offset != L.Anomalies.CCJumpOffset {
		t.Fatalf("jump offset=%d want %d", j.Offset, L.Anomalies.CCJumpOffset)
	}
	if j.RelatedKeyframeOffset != L.Anomalies.CCJumpOffset {
		t.Fatalf("jump not linked to its keyframe: related=%d", j.RelatedKeyframeOffset)
	}
	if j.IntervalID != "seg_0001" {
		t.Fatalf("jump interval=%q want seg_0001", j.IntervalID)
	}

	// Both representations of that interval must embed the keyframe boundary.
	found := 0
	for _, a := range m.Artifacts {
		if a.IntervalIndex != L.Anomalies.JumpInterval {
			continue
		}
		full, _ := manifest.Resolve(dir, a.RelPath)
		b, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		pf, err := fanout.ParseFragment(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(pf.MP4.KeyframeOff) != 1 || pf.MP4.KeyframeOff[0] != L.Anomalies.CCJumpOffset {
			t.Fatalf("packed keyframe offsets=%v", pf.MP4.KeyframeOff)
		}
		found++
	}
	if found != 2 {
		t.Fatalf("expected 2 rate fragments for jump interval, got %d", found)
	}
}

// TestPCRWrapPropagatesAcrossSegmentBoundary proves the 33-bit wrap is expanded
// to monotonic time and reaches the packaged boundary for interval 2.
func TestPCRWrapPropagatesAcrossSegmentBoundary(t *testing.T) {
	dir := t.TempDir()
	m := demoManifest(t, dir)

	wraps := issuesByCat(m, merr.CatPCRWrap)
	if len(wraps) != 1 {
		t.Fatalf("pcr_wrap=%d want 1", len(wraps))
	}
	if wraps[0].ClockTime != int64(clock.Modulus33) {
		t.Fatalf("wrap clock=%d want unwrapped %d", wraps[0].ClockTime, clock.Modulus33)
	}

	var boundary *manifest.IntervalRecord
	for i := range m.Intervals {
		if m.Intervals[i].Index == sample.DefaultConfig().WrapAtInterval {
			boundary = &m.Intervals[i]
		}
	}
	if boundary == nil || boundary.StartTick != clock.Modulus33 {
		t.Fatalf("boundary interval start=%+v", boundary)
	}

	for _, a := range m.Artifacts {
		if a.IntervalIndex != 2 {
			continue
		}
		full, _ := manifest.Resolve(dir, a.RelPath)
		b, _ := os.ReadFile(full)
		pf, err := fanout.ParseFragment(b)
		if err != nil {
			t.Fatal(err)
		}
		if pf.Header.StartTick != clock.Modulus33 || pf.MP4.StartTick != clock.Modulus33 {
			t.Fatalf("wrap did not reach pack: header=%d mp4=%d", pf.Header.StartTick, pf.MP4.StartTick)
		}
	}
}

// TestPSIVersionSwitchPausesInterval proves PAT/PMT switch + bad CRC produce a
// recorded pause/recovery that propagates to interval and artifact metadata.
func TestPSIVersionSwitchPausesInterval(t *testing.T) {
	dir := t.TempDir()
	m := demoManifest(t, dir)

	if sw := len(issuesByCat(m, merr.CatPSIVersionSwitch)); sw != 2 {
		t.Fatalf("psi_version_switch=%d want 2 (PAT+PMT)", sw)
	}
	inc := issuesByCat(m, merr.CatMappingIncomplete)
	if len(inc) != 1 || inc[0].IntervalID != "seg_0002" {
		t.Fatalf("incomplete_mapping=%+v want one on seg_0002", inc)
	}
	if inc[0].Recovery != merr.StateRecovered {
		t.Fatalf("pause should be recovered, state=%s", inc[0].Recovery)
	}
	var iv2 *manifest.IntervalRecord
	for i := range m.Intervals {
		if m.Intervals[i].ID == "seg_0002" {
			iv2 = &m.Intervals[i]
		}
	}
	if iv2 == nil || !iv2.MappingPaused || !iv2.MappingRecovered {
		t.Fatalf("interval pause flags wrong: %+v", iv2)
	}
	for _, a := range m.Artifacts {
		if a.IntervalIndex == 2 && (!a.MappingPaused || !a.MappingRecovered) {
			t.Fatalf("artifact %s missing propagated pause/recover flags", a.ID)
		}
	}
}

// TestUnresolvedMappingBlocksPackaging proves that when the new PMT never
// becomes valid, the affected interval is paused/blocked and not packaged.
func TestUnresolvedMappingBlocksPackaging(t *testing.T) {
	dir := t.TempDir()
	cfg := sample.DefaultConfig()
	cfg.Intervals = 3 // switch interval (2) is last: no later valid PMT
	cfg.UnresolvedPMTSwitch = true
	if _, err := recover.Demo(recover.Options{WorkDir: dir, Rates: 1, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Load(dir)
	for _, iv := range m.Intervals {
		if iv.Index >= 2 && !iv.MappingPaused {
			t.Fatalf("interval %d should be paused by unresolved mapping", iv.Index)
		}
	}
	for _, a := range m.Artifacts {
		if a.IntervalIndex >= 2 {
			t.Fatalf("blocked interval must not be packaged, got artifact %s", a.ID)
		}
	}
	if len(m.Artifacts) != 2 { // intervals 0 and 1, one rate
		t.Fatalf("artifacts=%d want 2", len(m.Artifacts))
	}
}
