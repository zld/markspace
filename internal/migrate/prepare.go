package migrate

import (
	"errors"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
)

// prepare loads the source and any existing rollback point, decides whether
// work remains, and runs the prepare and write stages.
func prepare(opts Options) (*migrationState, error) {
	if opts.Dir == "" || opts.SourceName == "" || opts.TargetName == "" {
		return &migrationState{report: &Report{}}, fault.Newf(fault.CodeMigrationStage, "", "migration needs a directory, source and target")
	}
	if opts.MaxAlignment <= 0 {
		opts.MaxAlignment = clock.Millisecond
	}
	st := &migrationState{
		opts:       opts,
		report:     &Report{Source: opts.SourceName, Target: opts.TargetName},
		pointPath:  filepath.Join(opts.Dir, pointName),
		tempPath:   filepath.Join(opts.Dir, tempName),
		targetPath: filepath.Join(opts.Dir, opts.TargetName),
	}
	src, err := os.ReadFile(filepath.Join(opts.Dir, opts.SourceName))
	if err != nil {
		return st, fault.Newf(fault.CodeMigrationStage, "", "cannot read source manifest: %v", err)
	}
	st.srcBytes = src
	st.srcDigest = digest(src)
	point, err := loadPoint(st.pointPath)
	if err != nil {
		return st, err
	}
	if point != nil && point.SourceDigest != st.srcDigest {
		if err := os.Remove(st.pointPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return st, fault.Newf(fault.CodeMigrationRollback, "", "cannot discard stale rollback point: %v", err)
		}
		point = nil
	} else if point != nil {
		st.report.Resumed = true
	}
	if point != nil && point.Stage == StageDone {
		same, err := verifyTarget(st.targetPath, st.srcBytes, opts, point)
		if err != nil {
			return st, err
		}
		if same {
			st.report.Stage = StageDone
			st.report.Noop = true
			st.report.Entries = point.Entries
			st.report.Alignment = clock.Time(point.AlignmentNS)
			st.report.Digest = point.TargetDigest
			return st, nil
		}
		point = nil
	}
	m, err := manifest.Decode(st.srcBytes)
	if err != nil {
		return st, err
	}
	st.m = m
	st.point = point
	if point != nil {
		st.startStage = point.Stage
	}
	if st.report.Noop {
		return st, nil
	}
	if st.startStage == "" {
		st.point = &Point{Stage: StagePrepare, SourceDigest: st.srcDigest, Entries: len(m.Items)}
		if err := st.stage(StagePrepare); err != nil {
			return st, err
		}
	}
	if st.needWrite() {
		if err := st.writeTemp(); err != nil {
			return st, err
		}
	} else if st.targetBytes, err = os.ReadFile(st.tempPath); err != nil {
		if err := st.writeTemp(); err != nil {
			return st, err
		}
	}
	alignment, err := st.verify()
	if err != nil {
		return st, err
	}
	st.point.Stage = StageVerify
	st.point.Entries = len(m.Items)
	st.point.AlignmentNS = alignment.Nanos()
	if err := st.stage(StageVerify); err != nil {
		return st, err
	}
	return st, nil
}
