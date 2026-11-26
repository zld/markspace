package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"streamcheck/core"
	"streamcheck/fixture"
	"streamcheck/inspect"
	"streamcheck/migrate"
	"streamcheck/pipeline"
)

// inspectStage walks both fMP4 representations, locates keyframes and
// corruption, and checks the truncated-box fixture fails structurally.
func inspectStage(work string) pipeline.Stage {
	return pipeline.Stage{Name: "inspect", Run: func(map[string]string) (string, error) {
		art := inspectArtifact{CorruptSegment: -1, CorruptOffset: -1}
		var allTimes [][]int64
		for _, name := range []string{fixture.FMP4Low, fixture.FMP4High} {
			path := filepath.Join(work, name)
			rep, err := inspect.InspectMP4(path, 1000)
			if err != nil {
				return "", err
			}
			data, err := readFixture(work, name)
			if err != nil {
				return "", err
			}
			var times []int64
			for i := range rep.Segments {
				seg := &rep.Segments[i]
				seg.AnalyzeAVC(data[seg.MdatOffset : seg.MdatOffset+seg.MdatSize])
				if seg.Keyframe {
					times = append(times, seg.DecodeTime.Millis())
				}
				if seg.Corrupt && art.CorruptSegment < 0 {
					art.CorruptSegment = seg.Index
					art.CorruptOffset = seg.CorruptOffset
				}
			}
			allTimes = append(allTimes, times)
		}
		gop := inspect.GOP(allTimes[0])
		art.KeyframeTimesMs = allTimes[0]
		art.GOPMaxMs = gop.MaxIntervalMs
		art.GOPNormalMaxMs = normalMax(gop.IntervalsMs)
		art.CrossRepKeyframes = inspect.CompareKeyframes(allTimes[0], allTimes[1]) == -1

		truncPath := filepath.Join(work, fixture.TruncatedFile)
		if _, err := os.Stat(truncPath); err == nil {
			_, err := inspect.InspectMP4(truncPath, 1000)
			if err == nil {
				return "", core.E(core.KindVerify, "inspect",
					"truncated fixture did not produce a structured error", nil)
			}
			art.TruncatedError = err.Error()
			var ce *core.Error
			if errors.As(err, &ce) {
				art.TruncatedOffset = ce.Offset
			}
		}
		return marshalArtifact(art)
	}}
}

// normalMax returns the largest GOP interval excluding the single largest
// one — the steady-state keyframe spacing without the injected anomaly.
func normalMax(intervals []int64) int64 {
	if len(intervals) == 0 {
		return 0
	}
	top := 0
	for i := range intervals {
		if intervals[i] > intervals[top] {
			top = i
		}
	}
	var m int64
	for i, v := range intervals {
		if i != top && v > m {
			m = v
		}
	}
	return m
}

// migrateStage first exercises the injected validation failure (fault
// set permitting) and proves byte-exact rollback, then runs the real
// migration to recovered.bin.
func migrateStage(work string, plan *fixture.Plan) pipeline.Stage {
	return pipeline.Stage{Name: "migrate", Run: func(map[string]string) (string, error) {
		legacyPath := filepath.Join(work, fixture.LegacyFile)
		before, err := readFixture(work, fixture.LegacyFile)
		if err != nil {
			return "", err
		}
		art := migrateArtifact{RecoveredFile: fixture.RecoveredFile}
		faults, err := fixture.ParseFaultSet(plan.FaultSet)
		if err != nil {
			return "", err
		}
		if faults.MigrateFail {
			art.RollbackChecked = true
			bad := &migrate.Migration{
				Src:       legacyPath,
				Dst:       filepath.Join(work, "bad.out"),
				Transform: transformLegacy,
				Validate: func([]byte) error {
					return errors.New("injected shadow validation failure")
				},
			}
			runErr := bad.Run()
			if runErr == nil {
				return "", core.E(core.KindVerify, "migrate",
					"injected validation failure did not trigger", nil)
			}
			var ce *core.Error
			if !errors.As(runErr, &ce) || ce.Kind != core.KindMigrate {
				return "", core.E(core.KindVerify, "migrate",
					"unexpected failure kind: "+runErr.Error(), nil)
			}
			after, err := readFixture(work, fixture.LegacyFile)
			if err != nil {
				return "", err
			}
			art.RollbackIdentical = bytes.Equal(before, after)
			if _, statErr := os.Stat(bad.Dst); !os.IsNotExist(statErr) {
				art.RollbackIdentical = false
			}
		}
		good := &migrate.Migration{
			Src:       legacyPath,
			Dst:       filepath.Join(work, fixture.RecoveredFile),
			Transform: transformLegacy,
			Validate:  validateNormalized,
		}
		if err := good.Run(); err != nil {
			return "", err
		}
		after, err := readFixture(work, fixture.LegacyFile)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(before, after) {
			return "", core.E(core.KindVerify, "migrate",
				"source manifest modified by migration", nil).At(fixture.LegacyFile, -1)
		}
		rec, err := readFixture(work, fixture.RecoveredFile)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(rec)
		art.RecoveredSHA256 = hex.EncodeToString(sum[:])
		art.RecoveredBytes = len(rec)
		return marshalArtifact(art)
	}}
}
