package main

import (
	"path/filepath"

	"cmafrepair/pkg/media/cache"
	"cmafrepair/pkg/media/checkpoint"
	"cmafrepair/pkg/media/fixture"
)

// runRepair drives a full, resumable repair of one dataset directory.
func runRepair(args []string) error {
	f := parseFlags(args)
	in, ok1 := f.string_("in")
	out, ok2 := f.string_("out")
	if !ok1 || !ok2 || in == "" || out == "" {
		return &exitError{code: exitInvalidInput, msg: "repair requires --in DIR --out DIR"}
	}
	resume := f.bools["resume"]
	interruptAfter := 0
	if s, ok := f.string_("interrupt-after"); ok {
		n, err := parseInt64(s)
		if err != nil || n < 0 {
			return &exitError{code: exitInvalidInput, msg: "invalid --interrupt-after"}
		}
		interruptAfter = int(n)
	}
	if !exists(in) {
		return &exitError{code: exitInvalidInput, msg: "input directory not found: " + in}
	}

	store, err := cache.NewStore(filepath.Join(out, ".cache"))
	if err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}
	cp := checkpoint.NewStore(out)
	if resume {
		if err := cp.Load(); err != nil {
			return &exitError{code: exitInvalidInput, msg: err.Error()}
		}
	}
	// Always discard leftovers from an interrupted run.
	if _, err := cp.CleanupTemp(); err != nil {
		return &exitError{code: exitUnsafe, msg: err.Error()}
	}

	reps, err := listRepDirs(in)
	if err != nil {
		return &exitError{code: exitInvalidInput, msg: err.Error()}
	}
	if len(reps) == 0 {
		return &exitError{code: exitInvalidInput, msg: "no representations in " + in}
	}

	var anchors fixture.AnchorFile
	if exists(filepath.Join(in, "anchors.json")) {
		anchors, err = fixture.LoadAnchors(filepath.Join(in, "anchors.json"))
		if err != nil {
			return &exitError{code: exitInvalidInput, msg: err.Error()}
		}
	}

	runner := &runner{
		in: in, out: out, reps: reps, anchors: anchors,
		cache: store, cp: cp,
		interruptAfter: interruptAfter, resume: resume,
	}
	report, runErr := runner.execute()
	runner.print(report)
	if runErr != nil {
		if ee, ok := runErr.(*exitError); ok {
			return ee
		}
		return &exitError{code: exitUnsafe, msg: runErr.Error()}
	}
	return nil
}
