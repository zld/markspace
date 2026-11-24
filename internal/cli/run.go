package cli

import (
	"mediapipeline/internal/pipeline"
)

// RunOne is the `run` subcommand.
func RunOne(work, input, output string, resume bool) (pipeline.Summary, error) {
	return pipeline.Run(work, input, output, pipeline.Options{Resume: resume})
}

// RecoverOne is the `recover` subcommand.
func RecoverOne(work, input, output string) (pipeline.RecoverySummary, error) {
	return pipeline.Recover(work, input, output)
}
