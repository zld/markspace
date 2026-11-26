package pipeline

import (
	"testing"

	"liveregress/internal/recover"
)

func newTestCheckpoint(t *testing.T, outRoot string) *recover.Checkpoint {
	t.Helper()
	return recover.New("test", "root", "case")
}
