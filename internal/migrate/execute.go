package migrate

import (
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
)

// finish performs the atomic replace and completes the rollback point.
func (s *migrationState) finish() (*Report, error) {
	if s.report.Noop {
		return s.report, nil
	}
	if s.startStage != StageReplace && s.startStage != StageDone {
		if existing, err := os.ReadFile(s.targetPath); err == nil {
			if err := writeFileAtomic(s.targetPath+backupSuffix, existing); err != nil {
				return nil, fault.Newf(fault.CodeMigrationRollback, "", "cannot write rollback copy: %v", err)
			}
		}
		if err := os.Rename(s.tempPath, s.targetPath); err != nil {
			return nil, fault.Newf(fault.CodeMigrationStage, "", "cannot replace target manifest: %v", err)
		}
		s.point.Stage = StageReplace
		s.point.TargetReady = true
		if err := s.stage(StageReplace); err != nil {
			return s.report, err
		}
	}
	s.point.Stage = StageDone
	if err := savePoint(s.pointPath, s.point); err != nil {
		return nil, err
	}
	s.report.Stage = StageDone
	s.report.Entries = s.point.Entries
	s.report.Alignment = clock.Time(s.point.AlignmentNS)
	s.report.Digest = s.point.TargetDigest
	return s.report, nil
}

func (s *migrationState) stage(name string) error {
	if err := savePoint(s.pointPath, s.point); err != nil {
		return err
	}
	s.report.Stage = name
	if s.opts.InterruptAt == name {
		return fault.Newf(fault.CodeMigrationInterrupted, "", "migration interrupted after stage %s; rerun with --resume", name)
	}
	return nil
}

func (s *migrationState) needWrite() bool {
	switch s.startStage {
	case "", StagePrepare, StageWrite:
		return true
	}
	return false
}

func (s *migrationState) writeTemp() error {
	b, err := manifest.EncodeCurrent(s.m, s.opts.Timescales)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := writeFileAtomic(s.tempPath, b); err != nil {
		return fault.Newf(fault.CodeMigrationStage, "", "cannot write temporary manifest: %v", err)
	}
	s.targetBytes = b
	s.point.Stage = StageWrite
	s.point.TargetDigest = digest(b)
	return s.stage(StageWrite)
}

func (s *migrationState) verify() (clock.Time, error) {
	back, err := manifest.Decode(s.targetBytes)
	if err != nil {
		return 0, err
	}
	if len(back.Items) != len(s.m.Items) {
		return 0, fault.Newf(fault.CodeManifestCount, "", "migrated manifest has %d entries, source has %d", len(back.Items), len(s.m.Items))
	}
	worst, err := manifest.AlignmentError(s.m, back)
	if err != nil {
		return 0, err
	}
	if worst > s.opts.MaxAlignment {
		return worst, fault.Newf(fault.CodeManifestAlignment, "", "timestamp alignment error %s exceeds %s", worst, s.opts.MaxAlignment)
	}
	return worst, nil
}
