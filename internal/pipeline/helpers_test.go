package pipeline

import (
	"time"

	"subsync/internal/check"
	"subsync/internal/report"
)

func fakeSummary(maxDeltaMS, lost int) report.SummaryRecord {
	return report.SummaryRecord{
		InputCount:  8,
		OutputCount: 8 - lost,
		LostCount:   lost,
		MaxDeltaMS:  int64(maxDeltaMS),
	}
}

func fakeReport(missing int) *check.Report {
	return &check.Report{
		Missing:  missing,
		MaxDelta: time.Duration(missing) * time.Millisecond,
	}
}
