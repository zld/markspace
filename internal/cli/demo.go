// Package cli implements the demo and verify commands.  It owns the fixture
// layout, the report format and the pass/fail policy; cmd/mediapipeline only
// parses flags.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/pipeline"
)

// Report is the persisted demo result.
type Report struct {
	Version int    `json:"version"`
	WorkDir string `json:"work_dir"`
	// Flat fields mirror the normal run so an acceptance check can read the
	// headline numbers without walking nested objects.
	Tracks           int                      `json:"tracks"`
	EventsIn         int                      `json:"events_in"`
	EventsOut        int                      `json:"events_out"`
	EventsDuplicate  int                      `json:"events_duplicate"`
	PayloadSHA256In  string                   `json:"payload_sha256_in"`
	PayloadSHA256Out string                   `json:"payload_sha256_out"`
	Normal           pipeline.Summary         `json:"normal"`
	TS               pipeline.Summary         `json:"ts"`
	Recovery         pipeline.RecoverySummary `json:"recovery"`
	Recovery2        pipeline.RecoverySummary `json:"recovery_second"`
	Damaged          []DamagedCase            `json:"damaged"`
	Checks           []Check                  `json:"checks"`
}

// DamagedCase is one expected-corruption result.
type DamagedCase struct {
	Name        string `json:"name"`
	Input       string `json:"input"`
	Expected    int64  `json:"expected_offset"`
	InputOffset int64  `json:"input_offset"`
	Delta       int64  `json:"offset_delta"`
	ErrorStage  string `json:"error_stage"`
	ErrorOp     string `json:"error_op"`
	Recognised  bool   `json:"recognised"`
}

// Check is one named assertion.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Demo generates the fixtures, runs every pipeline shape and writes
// work/report.json.  A non-nil error means at least one check failed.
func Demo(work string) (Report, error) {
	in := filepath.Join(work, "in")
	out := filepath.Join(work, "out")
	for _, dir := range []string{in, out} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Report{}, err
		}
	}
	rep := Report{Version: 1, WorkDir: work}
	// A fresh demo starts from a clean breakpoint so the recovery case is a
	// real first commit, not a resume of an earlier demo.
	for _, name := range []string{"checkpoint.json", "recovery.json"} {
		_ = os.Remove(filepath.Join(work, name))
	}

	valid := fixture.BuildValid(fixture.Options{EventDuplicates: true})
	if err := writeFixture(filepath.Join(in, "valid.mp4"), valid); err != nil {
		return rep, err
	}
	sum, err := pipeline.Run(work, filepath.Join(in, "valid.mp4"), filepath.Join(out, "valid.mp4"), pipeline.Options{})
	rep.Normal = sum
	rep.Tracks = sum.Tracks
	rep.EventsIn = sum.EventsIn
	rep.EventsOut = sum.EventsOut
	rep.EventsDuplicate = sum.Duplicates
	rep.PayloadSHA256In = sum.PayloadSHA256In
	rep.PayloadSHA256Out = sum.PayloadSHA256Out
	if err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "normal-run", OK: false, Detail: err.Error()})
		return finish(rep, work)
	}
	rep.Checks = append(rep.Checks,
		Check{Name: "normal-payload-match", OK: sum.PayloadSHA256In == sum.PayloadSHA256Out,
			Detail: sum.PayloadSHA256In + " / " + sum.PayloadSHA256Out},
		Check{Name: "normal-events-nonzero", OK: sum.EventsOut > 0, Detail: fmt.Sprintf("%d", sum.EventsOut)},
		Check{Name: "normal-duplicates-one", OK: sum.Duplicates == 1, Detail: fmt.Sprintf("%d", sum.Duplicates)},
	)

	ts := fixture.BuildTS()
	if err := writeFixture(filepath.Join(in, "valid.ts"), ts); err != nil {
		return rep, err
	}
	tsSum, err := pipeline.Run(work, filepath.Join(in, "valid.ts"), filepath.Join(out, "valid-ts.mp4"), pipeline.Options{})
	rep.TS = tsSum
	rep.Checks = append(rep.Checks, Check{
		Name: "ts-remux-payload-match", OK: err == nil && tsSum.PayloadSHA256In == tsSum.PayloadSHA256Out,
		Detail: errDetail(err),
	})

	rec, rec2, recoveredOK, recErr := runRecoveryCases(work, in, out)
	rep.Recovery, rep.Recovery2 = rec, rec2
	rep.Checks = append(rep.Checks, Check{
		Name: "recovery-stable", OK: recoveredOK, Detail: errDetail(recErr),
	})

	for _, dc := range damagedCases(in) {
		caseSum, caseErr := pipeline.Run(work, dc.path, filepath.Join(out, "damaged-"+dc.name+".mp4"), pipeline.Options{})
		got := DamagedCase{
			Name: dc.name, Input: dc.path, Expected: dc.offset,
			InputOffset: caseSum.InputOffset, ErrorStage: caseSum.ErrorStage,
			ErrorOp: caseSum.ErrorOp,
		}
		got.Delta = got.InputOffset - got.Expected
		if got.Delta < 0 {
			got.Delta = -got.Delta
		}
		got.Recognised = caseErr != nil && caseSum.ErrorStage != "" && !caseSum.Committed
		rep.Damaged = append(rep.Damaged, got)
		rep.Checks = append(rep.Checks, Check{
			Name: "damaged-" + dc.name, OK: got.Recognised && got.Delta == 0,
			Detail: fmt.Sprintf("stage=%s op=%s input_offset=%d expected=%d delta=%d", got.ErrorStage, got.ErrorOp, got.InputOffset, got.Expected, got.Delta),
		})
	}
	return finish(rep, work)
}

type damagedSpec struct {
	name   string
	path   string
	offset int64
}

func damagedCases(in string) []damagedSpec {
	var specs []damagedSpec
	trunc := fixture.BuildTruncatedBox(fixture.Options{})
	_ = writeFixture(filepath.Join(in, "truncated-box.mp4"), trunc)
	if trunc.Damage != nil {
		specs = append(specs, damagedSpec{"truncated-box", filepath.Join(in, "truncated-box.mp4"), trunc.Damage.Offset})
	}
	rtp := fixture.BuildRTP()
	_ = writeFixture(filepath.Join(in, "damaged-rtp.bin"), rtp)
	if rtp.Damage != nil {
		specs = append(specs, damagedSpec{"rtp-sequence", filepath.Join(in, "damaged-rtp.bin"), rtp.Damage.Offset})
	}
	missing := fixture.BuildMissingIndex(fixture.Options{})
	_ = writeFixture(filepath.Join(in, "missing-index.mp4"), missing)
	if missing.Damage != nil {
		specs = append(specs, damagedSpec{"missing-index", filepath.Join(in, "missing-index.mp4"), missing.Damage.Offset})
	}
	return specs
}

func runRecoveryCases(work, in, out string) (pipeline.RecoverySummary, pipeline.RecoverySummary, bool, error) {
	rec, _ := fixture.BuildRecoverable(fixture.Options{})
	path := filepath.Join(in, "recoverable.mp4")
	if err := writeFixture(path, rec); err != nil {
		return pipeline.RecoverySummary{}, pipeline.RecoverySummary{}, false, err
	}
	output := filepath.Join(out, "recoverable.mp4")
	first, err := pipeline.Recover(work, path, output)
	if err != nil {
		return first, pipeline.RecoverySummary{}, false, err
	}
	second, err := pipeline.Recover(work, path, output)
	if err != nil {
		return first, second, false, err
	}
	ok := first.PayloadSHA256In == first.PayloadSHA256Out &&
		second.PayloadSHA256Out == first.PayloadSHA256Out &&
		second.DuplicateSegments == 0 &&
		first.CommittedSegments == first.RecoveredSegments
	return first, second, ok, nil
}

func finish(rep Report, work string) (Report, error) {
	failed := 0
	for _, c := range rep.Checks {
		if !c.OK {
			failed++
		}
	}
	if err := os.WriteFile(filepath.Join(work, "report.json"), mustJSON(rep), 0o644); err != nil {
		return rep, err
	}
	if failed > 0 {
		return rep, fmt.Errorf("%d demo check(s) failed", failed)
	}
	return rep, nil
}
