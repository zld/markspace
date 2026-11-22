package testkit

import (
	"os"
	"path/filepath"
	"time"

	"mediacore/internal/clock"
)

// DemoParams mirrors the CLI demo flags without importing the main package.
type DemoParams struct {
	Duration   time.Duration
	TargetBPS  uint64
	BatchBytes int64
	SlowEvery  int
}

// Scenario is one generated fixture set plus the corruption (if any) applied
// before the pipeline runs.
type Scenario struct {
	Name       string
	Corruption Corruption
	FixtureSet FixtureSet
	Spec       MP4Spec
	Params     DemoParams
}

// PrepareRoot creates (or cleans) the top-level fixtures directory.
func PrepareRoot(root string) (string, error) {
	if root == "" {
		return "", clock.ErrInvalidArgument("testkit: empty fixture root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", clock.ErrRecoverablef("testkit.root", "mkdir: %v", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", clock.ErrRecoverablef("testkit.root", "abs: %v", err)
	}
	return abs, nil
}

const (
	demoFPS        = 30
	demoClockRate  = 90000
	demoFrameDelta = demoClockRate / demoFPS // 3000 ticks
)

// GenerateScenarios builds the normal fixture and the four corruption
// variants under root. Sample count derives from the requested duration at
// 30 fps; corruption variants use smaller fixtures to keep generation cheap
// while exercising the identical code paths.
func GenerateScenarios(root string, p DemoParams) ([]Scenario, error) {
	// Integer-only sample count at 30 fps: duration_ns * 30 / 1e9.
	fullCount := uint32((int64(p.Duration) * demoFPS) / int64(time.Second))
	if fullCount == 0 {
		return nil, clock.ErrInvalidArgument("testkit: duration too short for one frame")
	}
	normal, err := makeScenario(root, "normal", fullCount, p, "")
	if err != nil {
		return nil, err
	}
	scenarios := []Scenario{normal}

	smallCount := uint32(600)
	corrupt := []struct {
		name string
		kind Corruption
	}{
		{"truncated", CorruptTruncated},
		{"edit_oob", CorruptEditOOB},
		{"wrap", CorruptWrap},
		{"index_broken", CorruptIndexBroken},
		{"no_keyframe", CorruptNoKeyframe},
	}
	for _, c := range corrupt {
		sc, err := makeScenario(root, c.name, smallCount, p, c.kind)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, sc)
	}
	return scenarios, nil
}

func makeScenario(root, name string, count uint32, p DemoParams, kind Corruption) (Scenario, error) {
	fs, err := PrepareDirs(root, name)
	if err != nil {
		return Scenario{}, err
	}
	// A fresh demo run starts from sample zero even if an older run left a
	// checkpoint behind. The index cache is intentionally retained so a
	// repeat run can legitimately report index_cache=hit.
	for _, stale := range []string{fs.Checkpoint, fs.Output} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return Scenario{}, clock.ErrRecoverablef("testkit.clean", "remove %s: %v", stale, err)
		}
	}
	spec := MP4Spec{
		Path: fs.MP4, Manifest: fs.Manifest, TrackID: 1,
		Timescale: demoClockRate, SampleCount: count, SampleDelta: demoFrameDelta,
		KeyEvery: 60, MediaStartTicks: demoClockRate,
		TargetBPS: p.TargetBPS, JitterPct: 4, Seed: 20261123,
	}
	sc := Scenario{Name: name, Corruption: kind, FixtureSet: fs, Spec: spec, Params: p}
	if err := generateForScenario(sc); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

// generateForScenario materializes the MP4, manifest and RTP sidecar, then
// applies the scenario's corruption.
func generateForScenario(sc Scenario) error {
	switch sc.Corruption {
	case CorruptEditOOB:
		if err := WriteEditOutOfBounds(sc.Spec); err != nil {
			return err
		}
	case CorruptNoKeyframe:
		// Write a sparse stss in the MP4, then suppress every sync flag via
		// the manifest so the scanner sees a track with no decodable anchor.
		spec := sc.Spec
		spec.KeyEvery = 60
		spec.ForceNonSync = allSampleNumbers(spec.SampleCount)
		if err := WriteMP4(spec); err != nil {
			return err
		}
		if _, err := WriteManifest(spec); err != nil {
			return err
		}
	default:
		if err := WriteMP4(sc.Spec); err != nil {
			return err
		}
		if _, err := WriteManifest(sc.Spec); err != nil {
			return err
		}
	}

	// RTP sidecar: wrap scenario straddles 2^32, others stay linear.
	switch sc.Corruption {
	case CorruptWrap:
		if err := WriteRTPSidecar(sc.FixtureSet.RTP, 1, demoClockRate,
			sc.Spec.SampleCount, demoFrameDelta, sc.Spec.SampleCount/4, 0); err != nil {
			return err
		}
	default:
		if err := WriteRTPSidecar(sc.FixtureSet.RTP, 1, demoClockRate,
			sc.Spec.SampleCount, demoFrameDelta, 0, 0); err != nil {
			return err
		}
	}

	switch sc.Corruption {
	case CorruptTruncated:
		if err := TruncateMP4(sc.FixtureSet.MP4, physicalPayload*8+4); err != nil {
			return err
		}
		// Re-hash so the sidecar digest matches the truncated bytes.
		if _, err := WriteManifest(sc.Spec); err != nil {
			return err
		}
	case CorruptIndexBroken:
		// Seed a syntactically-sized but hash-invalid cache file. The
		// pipeline must reject and rebuild it.
		digest, err := scanDigest(sc.FixtureSet.Manifest)
		if err != nil {
			return err
		}
		path := CacheFileFor(sc.FixtureSet.Index, digest, 1)
		if err := seedBrokenCache(path, sc.Spec.SampleCount); err != nil {
			return err
		}
	}
	return nil
}
