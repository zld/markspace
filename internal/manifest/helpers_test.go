package manifest_test

import "mediapack/internal/clock"

func modelAlign() clock.AlignmentError {
	return clock.AlignmentError{MaxErrorTicks: 1, WorstRemainder: 42000, WorstDenom: 48000}
}
