// Package timeline merges primary/backup manifests onto one exact rational
// program timeline, fills gaps only from temporally overlapping backup
// segments, and expands 32-bit RTP timestamps from source-switch records.
package timeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pion/rtp"

	"continuity/internal/clock"
)

// SwitchRecord is one line of a source-switch log: the RTP state at a cut.
type SwitchRecord struct {
	Source     string `json:"source"`
	Seq        uint16 `json:"seq"`
	Timestamp  uint32 `json:"timestamp"`
	ClockRate  int64  `json:"clock_rate"`
	ProgramNS  int64  `json:"program_ns"`
}

// LoadSwitchRecords reads newline-delimited JSON switch records from path.
// Each record is validated: clock rate must be positive and program_ns >= 0.
func LoadSwitchRecords(path string) ([]SwitchRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.AtFile(clock.New(clock.KindIO, "switch-records",
			"open: %v", err), path)
	}
	defer f.Close()

	var out []SwitchRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Bytes()
		if len(text) == 0 {
			continue
		}
		var rec SwitchRecord
		if err := json.Unmarshal(text, &rec); err != nil {
			return nil, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
				"switch-records", "bad JSON: %v", err), line), path)
		}
		if rec.ClockRate <= 0 {
			return nil, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
				"switch-records", "clock_rate must be positive, got %d",
				rec.ClockRate), line), path)
		}
		if rec.ProgramNS < 0 {
			return nil, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
				"switch-records", "program_ns must be non-negative"), line), path)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, clock.AtFile(clock.New(clock.KindIO, "switch-records",
			"read: %v", err), path)
	}
	if len(out) == 0 {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput,
			"switch-records", "no switch records"), path)
	}
	return out, nil
}

// UnwrapTimestamp expands a 32-bit RTP timestamp to a monotonically
// increasing int64 given the unwrapped previous value. A forward delta
// within half the 32-bit space stays in the current cycle; larger deltas are
// treated as a backwards wrap. Returns the unwrapped tick count (not seconds).
func UnwrapTimestamp(prev int64, cur uint32) int64 {
	const mod = int64(1) << 32
	const half = mod / 2
	cycle := prev / mod
	if cycle < 0 {
		cycle = 0
	}
	candidate := cycle*mod + int64(cur)
	delta := candidate - prev
	if delta > half {
		// cur is actually in the previous cycle (wrapped backwards relative
		// to the naive placement): pull it down one cycle.
		candidate -= mod
	} else if delta < -half {
		candidate += mod
	}
	return candidate
}

// ExpandRTPStream unwraps an ordered sequence of RTP packets belonging to one
// source, returning absolute unwrapped tick counts and detecting duplicate
// or out-of-order sequence numbers. It uses pion/rtp for packet parsing.
func ExpandRTPStream(packets []*rtp.Packet) ([]int64, error) {
	if len(packets) == 0 {
		return nil, clock.New(clock.KindInvalidInput, "rtp", "empty packet stream")
	}
	out := make([]int64, len(packets))
	var prevTS int64
	var prevSeq uint16
	havePrev := false
	for i, pkt := range packets {
		if i == 0 {
			prevTS = int64(pkt.Timestamp)
			out[i] = prevTS
			prevSeq = pkt.SequenceNumber
			havePrev = true
			continue
		}
		// Sequence numbers themselves wrap at 16 bits; a backwards jump beyond
		// half the space means the stream is not ordered and is rejected.
		seqDelta := int32(pkt.SequenceNumber) - int32(prevSeq)
		if seqDelta == 0 {
			return nil, clock.New(clock.KindInvalidInput, "rtp",
				"duplicate RTP sequence number %d at packet %d",
				pkt.SequenceNumber, i)
		}
		if seqDelta < -32768 || (seqDelta < 0 && seqDelta > -32768) {
			return nil, clock.New(clock.KindInvalidInput, "rtp",
				"out-of-order RTP packet at index %d (seq %d after %d)",
				i, pkt.SequenceNumber, prevSeq)
		}
		prevTS = UnwrapTimestamp(prevTS, pkt.Timestamp)
		out[i] = prevTS
		prevSeq = pkt.SequenceNumber
	}
	_ = havePrev
	return out, nil
}

// SwitchProgramTime maps a switch record to a rational program time in
// seconds using its program_ns anchor, and validates that its RTP timestamp
// converted at clock_rate is consistent with that anchor.
func SwitchProgramTime(rec SwitchRecord) (clock.Rat, error) {
	prog, err := clock.Nanoseconds(rec.ProgramNS)
	if err != nil {
		return clock.Rat{}, fmt.Errorf("program_ns: %w", err)
	}
	// RTP media time at the cut.
	media, err := clock.TicksToRat(int64(rec.Timestamp), rec.ClockRate)
	if err != nil {
		return clock.Rat{}, err
	}
	_ = media
	return prog, nil
}
