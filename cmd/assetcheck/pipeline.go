package main

import (
	"fmt"
	"os"

	"example.com/assetcheck/internal/index"
	"example.com/assetcheck/internal/report"
	"example.com/assetcheck/internal/scan"
	"example.com/assetcheck/internal/stream"
	"example.com/assetcheck/internal/validate"
)

// process runs one asset through scan or index reuse, validation and
// streaming. It returns a populated run even when the asset fails, so the
// caller can report located faults before choosing an exit code.
func process(name string, in scan.Input, indexPath string, reuse bool) (*report.Run, error) {
	run := &report.Run{Scenario: name, State: report.IndexState{Path: indexPath}}
	digest, _ := index.ComputeDigest(in.MP4, in.RTP)

	var res *scan.Result
	if reuse {
		loaded, err := index.Load(indexPath)
		switch {
		case err == nil && loaded.Digest.Value == digest.Value:
			run.State.Loaded = true
			run.State.Reused = true
			run.State.Matched = true
			run.State.Digest = loaded.Digest
			run.Index = loaded
			res = &scan.Result{Index: loaded}
		case err == nil:
			run.State.Stale = true
			run.State.Reason = fmt.Sprintf("input digest %s differs from indexed %s",
				short(digest.Value), short(loaded.Digest.Value))
			res = scan.Rebuild(in)
		case os.IsNotExist(err):
			run.State.Reason = "no index on disk"
			res = scan.Rebuild(in)
		default:
			// A present but unreadable or structurally incomplete index is
			// treated the same as a stale one: rebuild and record why.
			run.State.Stale = true
			run.State.Reason = "index unusable: " + err.Error()
			res = scan.Rebuild(in)
		}
	} else {
		res = scan.Run(in)
	}

	run.Index = res.Index
	run.Scan = res.Faults
	run.State.Digest = res.Index.Digest
	if res.Index.Rebuilt {
		run.State.Rebuilt = true
	}
	// Only complete indexes are persisted: an incomplete index has no value
	// on reuse and would be rejected by Load's structural check anyway.
	if !run.State.Reused && indexPath != "" && res.Index.Complete {
		if err := res.Index.Save(indexPath); err != nil {
			return run, fmt.Errorf("saving index: %w", err)
		}
	}
	if res.Index.Complete {
		run.Validate = validate.Check(res.Index, in.MP4)
	}
	run.Stream = stream.Run(res.Index, in.MP4)
	return run, nil
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
