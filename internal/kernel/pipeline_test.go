package kernel

import "mediad/internal/pipeline"

func pipelineLoad(path, inputDigest, configDigest string) (*pipeline.Checkpoint, error) {
	return pipeline.LoadCheckpoint(path, inputDigest, configDigest)
}
