package orchestrator

import "os"

// mkdirAll creates the run's output and scratch directories. Paths have
// already been confined to the workspace root by safeio in Engine.New.
func mkdirAll(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
	}
	return nil
}
