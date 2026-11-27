package recovery

import (
	"fmt"
)

// ResumePlan reports the lowest boundary and each stage's own offset, which
// together describe where a restart begins.
func (s *Session) ResumePlan(stages ...string) (int64, map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	per := make(map[string]int64, len(stages))
	for _, name := range stages {
		per[name] = s.cp.Stage(name).Offset
	}
	return s.cp.ResumeOffset(stages...), per
}

// String renders a checkpoint summary for logs.
func (c *Checkpoint) String() string {
	return fmt.Sprintf("checkpoint(source=%s digest=%s status=%s stages=%d)", c.Source, short(c.SourceDigest), c.Status, len(c.Stages))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
