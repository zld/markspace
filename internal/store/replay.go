package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// replay rebuilds job states; unfinished survivors become queued again.
func replay(jPath string, cp *Checkpoint) (int, error) {
	f, err := os.Open(jPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: open journal for replay: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	requeued := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return 0, fmt.Errorf("store: corrupt journal line: %w", err)
		}
		applyEvent(cp, &ev)
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("store: journal scan: %w", err)
	}
	for _, j := range cp.Jobs {
		if j.State == StateRunning || j.State == StateQueued || j.State == StateRetryable {
			j.State = StateQueued
			requeued++
		}
	}
	return requeued, nil
}

func applyEvent(cp *Checkpoint, ev *Event) {
	switch ev.Kind {
	case EvJobCreated:
		cp.Jobs[ev.JobID] = &Job{ID: ev.JobID, Segment: ev.Segment, QP: ev.QP,
			State: StateQueued, MaxAttempt: 2, UpdatedAt: ev.Timestamp}
	case EvClaim:
		if j := cp.Jobs[ev.JobID]; j != nil {
			j.State = StateRunning
			j.Attempts++
			j.UpdatedAt = ev.Timestamp
		}
	case EvRetry:
		if j := cp.Jobs[ev.JobID]; j != nil {
			j.State = StateRetryable
			j.LastError = ev.Error
			j.UpdatedAt = ev.Timestamp
		}
	case EvCompleted:
		if j := cp.Jobs[ev.JobID]; j != nil {
			j.State = StateCompleted
			if ev.Metrics != nil {
				j.Metrics = *ev.Metrics
			}
			j.UpdatedAt = ev.Timestamp
		}
	case EvFailed:
		if j := cp.Jobs[ev.JobID]; j != nil {
			j.State = StateFailed
			j.LastError = ev.Error
			j.UpdatedAt = ev.Timestamp
		}
	case EvRunInterrupt:
		cp.Interrupted = true
	}
}

func paramsEqual(a, b Parameters) bool {
	return a.SourceFingerprint == b.SourceFingerprint && a.Width == b.Width &&
		a.Height == b.Height && a.FPSNum == b.FPSNum && a.FPSDen == b.FPSDen &&
		a.Segments == b.Segments && a.TargetKbps == b.TargetKbps &&
		a.FramesPerSegment == b.FramesPerSegment && a.TotalFrames == b.TotalFrames
}
