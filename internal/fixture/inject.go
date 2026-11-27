package fixture

// CrashPoint names the fault-injection point used by the demo command.
type CrashPoint string

const (
	CrashNone          CrashPoint = ""
	CrashAfterArtifact CrashPoint = "after-artifact"
	CrashAfterCommit   CrashPoint = "after-commit"
)

// ParseCrashPoint validates a --inject-crash value.
func ParseCrashPoint(s string) (CrashPoint, bool) {
	switch CrashPoint(s) {
	case CrashNone:
		return CrashNone, true
	case CrashAfterArtifact:
		return CrashAfterArtifact, true
	case CrashAfterCommit:
		return CrashAfterCommit, true
	}
	return CrashNone, false
}

// Crash is the injected-exit signal. It is treated as an ordered shutdown, not
// a panic, so artifacts and checkpoints already written stay on disk exactly as
// a real process death would leave them.
type Crash struct{ Point CrashPoint }

func (c *Crash) Error() string {
	return "injected crash at " + string(c.Point)
}
