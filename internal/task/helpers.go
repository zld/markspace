package task

import (
	"math"
	"os"
	"path/filepath"
)

// ErrCheckpointNotFound is returned when no checkpoint exists for a key.
var ErrCheckpointNotFound = checkpointErr{"checkpoint not found"}

type checkpointErr struct{ msg string }

func (e checkpointErr) Error() string { return e.msg }

// readStagePayload loads the raw bytes recorded for a completed stage.
func readStagePayload(root string, cp *Checkpoint, st Stage) ([]byte, error) {
	res, ok := cp.Stages[st]
	if !ok {
		return nil, checkpointErr{"stage not recorded: " + string(st)}
	}
	return os.ReadFile(filepath.Join(TaskDir(root, cp.TaskKey), res.PayloadFile))
}

func bytesToF32(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := uint32(b[4*i]) | uint32(b[4*i+1])<<8 |
			uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
		out[i] = math.Float32frombits(bits)
	}
	return out
}

func f32ToBytes(s []float32) []byte {
	out := make([]byte, len(s)*4)
	for i, v := range s {
		bits := math.Float32bits(v)
		out[4*i] = byte(bits)
		out[4*i+1] = byte(bits >> 8)
		out[4*i+2] = byte(bits >> 16)
		out[4*i+3] = byte(bits >> 24)
	}
	return out
}
