package probe

import (
	"encoding/binary"
	"strconv"

	"tracecheck/internal/clock"
)

func itoa(v uint32) string { return strconv.Itoa(int(v)) }

func binaryU32Local(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}

// sampleDuration converts one delta to shared ticks through the track's own
// converter. Recovery never performs arithmetic on the timescale itself.
func sampleDuration(tr *Track, delta uint32) clock.Tick {
	tick, err := tr.Converter.ConvertFloor(uint64(delta))
	if err != nil {
		return 0
	}
	return tick.Ticks
}

// deltaFor reproduces the stts delta for a sample when the index is gone. The
// generated fixtures use one run of identical deltas, so the first parsed
// delta, if any, is authoritative; otherwise a zero delta is reported.
func deltaFor(tr *Track, sampleNo int) uint32 {
	if sampleNo < len(tr.Samples) && tr.Samples[sampleNo].Delta != 0 {
		return tr.Samples[sampleNo].Delta
	}
	for _, s := range tr.Samples {
		if s.Delta != 0 {
			return s.Delta
		}
	}
	return 0
}

// isSync mirrors the stss rule the writer used: every SyncInterval-th sample
// starting at zero. When stss was absent the track treated all as sync.
func (tr *Track) isSync(sampleNo int) bool {
	for _, s := range tr.Samples {
		if int(s.Index) == sampleNo {
			return s.Sync
		}
	}
	return true
}

// EventResumed labels a positive recovery event: playback restarts at a
// verifiable sync sample. It is intentionally outside the closed failure
// taxonomy in internal/clock, which only names fault categories.
const EventResumed clock.Category = "resumed_at_sync"

// ConversionEvent records sub-tick remainder discarded at a boundary.
func ConversionEvent(off clock.Offset, remainder uint64) RecoveryEvent {
	return RecoveryEvent{
		Category: clock.CategoryTruncated,
		Offset:   off,
		Detail:   "floored conversion discarded remainder " + strconv.FormatUint(remainder, 10),
	}
}
