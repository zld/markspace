package pipeline

import (
	"mediacore/internal/bitstream"
	"mediacore/internal/fault"
	"mediacore/internal/media"
)

// stageMigrate writes the legacy manifest and runs the staged migration,
// recording the rollback-aware outcome.
func stageMigrate(rep *Report, opts Options, stream *bitstream.Stream, syn media.Synthetic) error {
	mig, err := runMigration(opts, stream, syn)
	if err != nil {
		if f, ok := fault.As(err); ok {
			rep.Decision = "interrupted"
			rep.DecisionReason = f.Error()
		}
		rep.computeDigest()
		return err
	}
	rep.MigrationStage = mig.Stage
	rep.MigrationResumed = mig.Resumed
	rep.MigrationNoop = mig.Noop
	rep.ManifestEntries = mig.Entries
	rep.ManifestTimeErrMS = round3(mig.AlignmentMs())
	rep.ManifestDigest = mig.Digest
	return nil
}
