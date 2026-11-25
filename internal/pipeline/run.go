package pipeline

import (
	mediaclock "mediacore/internal/clock"
)

// Run executes the currently assembled pipeline stages.
func (rn *Runner) Run() (*Report, error) {
	st, err := rn.Store.Load()
	if err != nil {
		return nil, err
	}
	if st.BatchID == "" {
		st.BatchID = rn.BatchID
	}
	rn.State = st
	rn.Report.BatchID = rn.BatchID
	rn.Report.ManifestPath = rn.ManifestPath
	rn.Report.Range = rn.Range

	if err := rn.runParse(); err != nil {
		return rn.reportWithError(err), err
	}
	if err := rn.runTransform(); err != nil {
		return rn.reportWithError(err), err
	}
	rep := rn.Report
	return &rep, nil
}

func (rn *Runner) reportWithError(err error) *Report {
	if ce, ok := mediaclock.As(err); ok {
		rn.Report.ErrorOffsets = append(rn.Report.ErrorOffsets, ErrorOffset{
			Category: string(ce.Category), Path: ce.Path, Track: ce.Track, Offset: ce.Offset,
		})
	}
	rep := rn.Report
	return &rep
}
