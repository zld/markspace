package verify

import "mediasync/internal/recovery"

// recoveryReportStub builds a report whose recovery rate is 0.98:
// 49 recovered packets against 1 unrecoverable loss.
func recoveryReportStub() recovery.Report {
	return recovery.Report{
		Received:       50,
		UniqueSeqs:     49,
		Recovered:      49,
		Unrecoverable:  1,
		Losses: []recovery.LossWindow{{
			FirstSeq: 7, LastSeq: 7, Count: 1, Recoverable: false,
		}},
	}
}
