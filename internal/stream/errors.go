package stream

import (
	"mediapipeline/internal/fault"
)

func errInvalidWorkers() error {
	return fault.New(fault.CodeInvalidArgument, "worker count must be >= 1")
}

func errNilProcessor() error {
	return fault.New(fault.CodeInvalidArgument, "processor must not be nil")
}
