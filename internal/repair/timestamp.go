// Package repair fixes decode timestamps: duplicates are advanced by the
// sample duration, small backward jumps are bumped forward, and 32-bit
// RTP clock wraparound is unwrapped using the RTP sequence number and
// clock base. When a jump cannot be classified, the track is stopped and
// the already-completed samples are preserved.
package repair

import (
	"fmt"

	"github.com/pion/rtp"

	"mediarun/internal/fault"
	"mediarun/internal/parse"
)

// Reason classifies a timestamp repair.
type Reason string

const (
	ReasonDuplicate Reason = "duplicate"
	ReasonBackward  Reason = "backward"
	ReasonWrap      Reason = "wrap"
)

// Entry records one repaired sample for the provenance trail.
type Entry struct {
	Sample int    `json:"sample"` // global sample index in the track
	Old    uint64 `json:"old"`    // raw decode time as read (track units)
	New    uint64 `json:"new"`    // repaired decode time (track units, unwrapped)
	Why    Reason `json:"why"`
}

// Report is the repair record of one track.
type Report struct {
	Track     string  `json:"track"`
	Entries   []Entry `json:"entries"`
	Stopped   bool    `json:"stopped"`
	Completed int     `json:"completed"` // samples completed before a stop
}

// Count returns the number of repaired timestamps.
func (r *Report) Count() int { return len(r.Entries) }

// halfRange is half of the 32-bit RTP timestamp range; a drop larger
// than this with an advancing sequence number means wraparound.
const halfRange uint32 = 1 << 31

// Repairer holds the per-track repair state across fragments.
type Repairer struct {
	Track       string
	Timescale   int64
	RTP         bool  // treat raw timestamps as 32-bit RTP clock values
	MaxBackward int64 // largest backward jump (track units) repaired silently

	started bool
	cycles  uint64
	lastHdr rtp.Header // last accepted RTP header (sequence + timestamp)
	lastOut uint64     // last emitted unwrapped decode time
	lastDur uint64     // duration of the last emitted sample
	global  int        // global sample counter
}

// New creates a Repairer. rtpLike enables RTP wrap handling; maxBackward
// bounds the backward jump that is still repaired instead of stopping.
func New(track string, timescale int64, rtpLike bool, maxBackward int64) *Repairer {
	return &Repairer{Track: track, Timescale: timescale, RTP: rtpLike, MaxBackward: maxBackward}
}

// RepairFragment repairs all samples of one fragment in decode order and
// returns their repaired decode times in track units. On an
// undeterminable jump it stops the track: the report keeps the completed
// samples and the error names the fragment and its raw offset.
func (r *Repairer) RepairFragment(frag *parse.Fragment, rep *Report) ([]uint64, error) {
	out := make([]uint64, 0, len(frag.Samples))
	for _, s := range frag.Samples {
		v, err := r.repairOne(s.DecodeTime, uint64(s.Duration), rep)
		if err != nil {
			rep.Stopped = true
			return out, err.At(frag.Path, frag.MoofOffset).For(r.Track, r.global)
		}
		out = append(out, v)
		r.global++
		rep.Completed = r.global
	}
	return out, nil
}

// repairOne classifies and fixes one raw timestamp.
func (r *Repairer) repairOne(raw, dur uint64, rep *Report) (uint64, *fault.Error) {
	unwrapped := raw
	if r.RTP {
		hdr := rtp.Header{SequenceNumber: uint16(r.global), Timestamp: uint32(raw)}
		if r.started {
			if hdr.SequenceNumber != r.lastHdr.SequenceNumber+1 {
				return 0, fault.New(fault.StageRepair,
					fmt.Sprintf("sequence jump %d -> %d, cannot classify timestamp",
						r.lastHdr.SequenceNumber, hdr.SequenceNumber))
			}
			if hdr.Timestamp < r.lastHdr.Timestamp &&
				r.lastHdr.Timestamp-hdr.Timestamp > halfRange {
				r.cycles++ // 32-bit clock wrapped forward
				rep.Entries = append(rep.Entries, Entry{
					Sample: r.global, Old: raw, New: (r.cycles << 32) | uint64(hdr.Timestamp), Why: ReasonWrap,
				})
			}
		}
		r.lastHdr = hdr
		unwrapped = (r.cycles << 32) | uint64(hdr.Timestamp)
	}

	if r.started && unwrapped <= r.lastOut {
		why := ReasonDuplicate
		if unwrapped < r.lastOut {
			why = ReasonBackward
			if r.MaxBackward >= 0 && r.lastOut-unwrapped > uint64(r.MaxBackward) {
				return 0, fault.New(fault.StageRepair,
					fmt.Sprintf("backward jump of %d units exceeds limit, cannot determine timeline",
						r.lastOut-unwrapped))
			}
		}
		candidate := r.lastOut + r.lastDur
		if candidate <= r.lastOut {
			return 0, fault.New(fault.StageRepair,
				"duplicate timestamp with zero sample duration, cannot advance")
		}
		rep.Entries = append(rep.Entries, Entry{Sample: r.global, Old: raw, New: candidate, Why: why})
		unwrapped = candidate
	}

	r.started = true
	r.lastOut = unwrapped
	r.lastDur = dur
	return unwrapped, nil
}
