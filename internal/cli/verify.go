package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/pipeline"
)

// VerifyResult is the verify command output.
type VerifyResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Verify re-runs every persisted output check from work/report.json.
func Verify(work string) (VerifyResult, error) {
	buf, err := os.ReadFile(filepath.Join(work, "report.json"))
	if err != nil {
		return VerifyResult{}, err
	}
	var rep Report
	if err := json.Unmarshal(buf, &rep); err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{OK: true}
	add := func(name string, ok bool, detail string) {
		res.Checks = append(res.Checks, Check{Name: name, OK: ok, Detail: detail})
		if !ok {
			res.OK = false
		}
	}

	type outputCase struct {
		name string
		path string
		isTS bool
	}
	cases := []outputCase{
		{"normal-output", filepath.Join(work, "out", "valid.mp4"), false},
		{"ts-output", filepath.Join(work, "out", "valid-ts.mp4"), true},
		{"recovery-output", filepath.Join(work, "out", "recoverable.mp4"), false},
	}
	expects := []pipeline.Summary{rep.Normal, rep.TS, {
		Tracks: rep.Recovery.Tracks, EventsOut: rep.Recovery.EventsOut,
		PayloadSHA256In:  rep.Recovery.PayloadSHA256In,
		EventOrderSHA256: rep.Recovery.EventOrderSHA256,
	}}
	for i, c := range cases {
		want := pipeline.Verification{
			ExpectTracks: expects[i].Tracks, ExpectEvents: expects[i].EventsOut,
			PayloadSHA256:    expects[i].PayloadSHA256In,
			EventOrderSHA256: expects[i].EventOrderSHA256, Format: "mp4",
		}
		out, err := pipeline.InspectFile(c.path, want)
		if err != nil {
			add(c.name, false, err.Error())
			continue
		}
		add(c.name, true, fmt.Sprintf("tracks=%d events=%d payload=%s", out.Tracks, out.Events, out.PayloadSHA256))
	}
	add("normal-duplicates-one", rep.Normal.Duplicates == 1, fmt.Sprintf("%d", rep.Normal.Duplicates))
	// Re-run every damaged input instead of trusting the persisted verdict.
	scratch := filepath.Join(work, "verify-tmp")
	for _, d := range rep.Damaged {
		sum, err := pipeline.Run(scratch, d.Input, filepath.Join(scratch, "damaged-out.mp4"), pipeline.Options{})
		delta := sum.InputOffset - d.Expected
		if delta < 0 {
			delta = -delta
		}
		ok := err != nil && sum.ErrorStage != "" && !sum.Committed && delta == 0
		add("damaged-"+d.Name, ok,
			fmt.Sprintf("stage=%s op=%s input_offset=%d expected=%d delta=%d", sum.ErrorStage, sum.ErrorOp, sum.InputOffset, d.Expected, delta))
	}

	// Re-run recovery: the committed payload must be unchanged and no segment
	// may be written twice.
	if rec, err := pipeline.Recover(work, filepath.Join(work, "in", "recoverable.mp4"),
		filepath.Join(work, "out", "recoverable.mp4")); err != nil {
		add("recovery-rerun", false, err.Error())
	} else {
		add("recovery-rerun",
			rec.Committed && rec.DuplicateSegments == 0 &&
				rec.CommittedSegments == rec.RecoveredSegments &&
				rec.PayloadSHA256Out == rep.Recovery.PayloadSHA256Out,
			fmt.Sprintf("committed=%d recovered=%d duplicates=%d payload=%s",
				rec.CommittedSegments, rec.RecoveredSegments, rec.DuplicateSegments, rec.PayloadSHA256Out))
	}
	if err := os.WriteFile(filepath.Join(work, "verify.json"), mustJSON(res), 0o644); err != nil {
		return res, err
	}
	if !res.OK {
		return res, fmt.Errorf("verification failed")
	}
	return res, nil
}

func mustJSON(v interface{}) []byte {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil
	}
	return append(buf, '\n')
}

func writeFixture(path string, p fixture.Payload) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, p.Data, 0o644)
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
