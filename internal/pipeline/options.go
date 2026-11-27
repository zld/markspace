// Package pipeline: run options and their validation.
package pipeline

import (
	"path/filepath"
	"strings"

	"example.com/assetpack/internal/fault"
)

func (o Options) validate() error {
	if o.WorkDir == "" {
		return fault.New(fault.StageCLI, fault.KindInvalidArg, "work directory is required")
	}
	if err := o.Target.Validate(); err != nil {
		return err
	}
	if o.Release == "" {
		return fault.New(fault.StageCLI, fault.KindInvalidArg, "release name is required")
	}
	for _, p := range []string{o.InputDir, o.ReleaseDir, o.StateDir} {
		if !within(o.WorkDir, p) {
			return fault.New(fault.StageCLI, fault.KindPath,
				"path %q escapes work directory %q", p, o.WorkDir)
		}
	}
	return nil
}

// within reports whether child resolves inside parent.
func within(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	absP, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	absC, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absP, absC)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
