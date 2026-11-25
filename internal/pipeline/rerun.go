package pipeline

import (
	"strconv"
	"strings"

	"mediad/internal/rtp"
	"mediad/internal/stream"
)

// parseSegmentID extracts the numeric id from a "seg-000123.mp4" file name.
func parseSegmentID(name string) (int, bool) {
	const prefix, suffix = "seg-", ".mp4"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, false
	}
	id, err := strconv.Atoi(name[len(prefix) : len(name)-len(suffix)])
	if err != nil {
		return 0, false
	}
	return id, true
}

// ResetForRerun replaces the queue and RTP buffer so a recovery run starts
// from a clean transport state while preserving the loaded checkpoint.
func (e *Engine) ResetForRerun() {
	e.pipe = stream.NewPipe[*WorkChunk](e.cfg.QueueCapacity)
	e.rb = rtp.NewReorderBuffer(e.cfg.SampleRate, 1, e.cfg.MaxReorder)
	e.dec = nil
}
