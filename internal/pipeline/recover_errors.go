package pipeline

import (
	"mediapipeline/internal/fault"
)

func failRecovery(sum RecoverySummary, err error) RecoverySummary {
	if fe, ok := fault.As(err); ok {
		sum.ErrorStage = string(fe.Stage)
		sum.Reason = fe.Reason
		if fe.HasOffset() {
			sum.InputOffset = fe.InputOffset
		}
	} else if err != nil {
		sum.Reason = err.Error()
	}
	return sum
}
