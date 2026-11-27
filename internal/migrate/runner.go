package migrate

import (
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
)

// Options configures a migration run.
type Options struct {
	// Dir holds the manifest and its rollback point.
	Dir string
	// SourceName is the input manifest file name inside Dir.
	SourceName string
	// TargetName is the migrated manifest file name inside Dir.
	TargetName string
	// Timescales maps track names for the current-schema encoder.
	Timescales map[string]int64
	// MaxAlignment is the largest tolerated timestamp drift.
	MaxAlignment clock.Time
	// InterruptAt, when non-empty, simulates a crash after that stage so the
	// resume path can be exercised deterministically.
	InterruptAt string
}

// DefaultOptions returns the documented local layout.
func DefaultOptions(dir string) Options {
	return Options{
		Dir:          dir,
		SourceName:   "manifest.v1.json",
		TargetName:   "manifest.v2.json",
		Timescales:   map[string]int64{"video": 90000, "audio": 48000},
		MaxAlignment: clock.Millisecond,
	}
}

// Run executes or resumes the migration. A suspended run returns a
// *fault.Fault with code MC_MIGRATION_INTERRUPTED and exit status 3.
// verifyTarget confirms the committed target still matches the recorded
// digest and still aligns with the source manifest.
func verifyTarget(targetPath string, srcBytes []byte, opts Options, point *Point) (bool, error) {
	got, err := os.ReadFile(targetPath)
	if err != nil {
		return false, nil
	}
	if digest(got) != point.TargetDigest {
		return false, nil
	}
	src, err := manifest.Decode(srcBytes)
	if err != nil {
		return false, err
	}
	dst, err := manifest.Decode(got)
	if err != nil {
		return false, err
	}
	if len(src.Items) != len(dst.Items) {
		return false, nil
	}
	worst, err := manifest.AlignmentError(src, dst)
	if err != nil {
		return false, nil
	}
	if worst > opts.MaxAlignment {
		return false, fault.Newf(fault.CodeManifestAlignment, "", "committed manifest drifts by %s", worst)
	}
	return true, nil
}

func Run(opts Options) (*Report, error) {
	state, err := prepare(opts)
	if err != nil {
		return state.report, err
	}
	return state.finish()
}
