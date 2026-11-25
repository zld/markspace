package align

import (
	"mediapipeline/internal/fault"
)

func errNeedTwoRepresentations() error {
	return fault.New(fault.CodeInvalidArgument,
		"alignment requires at least two representations")
}
