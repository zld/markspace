package pipeline

import "mediascan/internal/model"

func exitCodeFor(rep *model.Report) int {
	if model.HasErrors(rep.Faults) {
		return 2
	}
	return 0
}

// finalizeReport aggregates segment data into the canonical report and picks
// the primary (first error-severity) locator for terminal output.
func finalizeReport(rep *model.Report, manifestFaults []model.Fault, res *Result) {
	var all []model.Fault
	all = append(all, manifestFaults...)
	var totalSamples int64
	periods := 1
	for i := range rep.Segments {
		s := &rep.Segments[i]
		totalSamples += s.SampleCount
		if s.Period+1 > periods {
			periods = s.Period + 1
		}
		all = append(all, s.Faults...)
	}
	rep.TotalSamples = totalSamples
	rep.SegmentCount = len(rep.Segments)
	rep.PeriodCount = periods
	rep.Faults = dedupeFaults(model.SortedFaults(all))
	rep.ErrorCount, rep.WarningCount = 0, 0
	for _, f := range rep.Faults {
		if f.Severity == model.SeverityError {
			rep.ErrorCount++
		} else {
			rep.WarningCount++
		}
	}
	rep.GOPMinTicks, rep.GOPMaxTicks, rep.GOPHistogram = model.AggregateGOP(rep.Segments)
	sha, _ := rep.ComputeSHA256()
	rep.SHA256 = sha

	for _, f := range rep.Faults {
		if f.Severity == model.SeverityError {
			res.PrimaryOffset = f.PrimaryOffset()
			res.PrimaryTime = f.PrimaryTime()
			break
		}
	}
}

func driftFromSegments(ss []model.SegmentReport) int64 {
	for _, s := range ss {
		if s.DriftPPM != 0 {
			return s.DriftPPM
		}
	}
	return 0
}

func wrapFromSegments(ss []model.SegmentReport) int {
	n := 0
	for _, s := range ss {
		n += s.WrapEvents
	}
	return n
}
