package demo

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/worker"
)

// deterministicProcess is the unit transform.  It is a pure function of the
// task key and payload, so replaying a checkpoint always reproduces identical
// output bytes and an interrupted resume converges on the full-run digest.
func deterministicProcess(t worker.Task) ([]byte, error) {
	sum := sha256.Sum256(t.Payload)
	return []byte(fmt.Sprintf("%s|%d|%x", t.Key, len(t.Payload), sum)), nil
}

// checkpointPath is the single checkpoint file used by a state directory.
func checkpointPath(cfg Config) string {
	return filepath.Join(cfg.StateDir, "checkpoint.json")
}

// readStaged reads the canonical bytes the stager wrote, which the validator
// re-hashes independently.
func readStaged(st interface {
	StagedPath() string
}) ([]byte, error) {
	return os.ReadFile(st.StagedPath())
}

// appendFaultLines converts structured faults into the report line shape.
func appendFaultLines(dst []DamageLine, faults []fault.Fault) []DamageLine {
	for _, f := range faults {
		dst = append(dst, DamageLine{
			Offset: f.Offset, Code: string(f.Code), Msg: f.Message,
		})
	}
	return dst
}

// joinReasons renders validator fault messages into one rollback reason.
func joinReasons(faults []fault.Fault) string {
	parts := make([]string, 0, len(faults))
	for _, f := range faults {
		parts = append(parts, string(f.Code)+": "+f.Message)
	}
	return strings.Join(parts, "; ")
}
