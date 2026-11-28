// Package migrate runs staged manifest migrations. Each stage writes
// its output into a staging area and must pass validation before the
// next stage runs; a failure leaves the source manifest untouched and
// keeps the staging area for inspection.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"

	"mediacheck/internal/fault"
)

// Step is one migration stage.
type Step struct {
	Name  string
	Apply func(dir string) error // produce staged output in dir
	Check func(dir string) error // validate staged output
}

// Run executes the steps in order inside staging. If interruptAfter
// names a step, Run stops right after that stage validates and
// returns an E_INTERRUPTED error, simulating a crash mid-migration.
func Run(steps []Step, staging string, interruptAfter string) error {
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	for _, s := range steps {
		dir := filepath.Join(staging, s.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := s.Apply(dir); err != nil {
			return fmt.Errorf("migrate stage %s apply: %w", s.Name, err)
		}
		if err := s.Check(dir); err != nil {
			return fmt.Errorf("migrate stage %s check: %w", s.Name, err)
		}
		if s.Name == interruptAfter {
			return fault.New(fault.ErrInterrupted, fault.NoOffset,
				"interrupted after migrate stage %s", s.Name)
		}
	}
	return nil
}
