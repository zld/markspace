package rtp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/pion/rtp"

	"archivecheck/internal"
)

// Event is the normalized JSONL ingest record.
type Event struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Track     string `json:"track"`
	Seq       uint16 `json:"seq"`
	PTSNS     int64  `json:"pts_ns"`
	ArrivalNS int64  `json:"arrival_ns"`
	Digest    string `json:"digest"`
}

// Disposition describes how a sequence observation changed tracker state.
type Disposition string

const (
	DispositionFirst      Disposition = "first"
	DispositionSequential Disposition = "sequential"
	DispositionGap        Disposition = "gap"
	DispositionOutOfOrder Disposition = "out_of_order"
	DispositionRecovered  Disposition = "recovered"
	DispositionDuplicate  Disposition = "duplicate"
)

// Observation is the result of feeding one event to a TrackTracker.
type Observation struct {
	Event        Event       `json:"event"`
	TrackKey     string      `json:"track_key"`
	ExtendedSeq  uint64      `json:"extended_seq"`
	Disposition  Disposition `json:"disposition"`
	Missing      []uint64    `json:"missing,omitempty"`
	GapDetected  int         `json:"gap_detected,omitempty"`
}

// TrackStats contains sequence quality counters for one source/track.
type TrackStats struct {
	Source       string `json:"source"`
	Track        string `json:"track"`
	Received     int64  `json:"received"`
	Unique       int64  `json:"unique"`
	Duplicates   int64  `json:"duplicates"`
	OutOfOrder   int64  `json:"out_of_order"`
	LossDetected int64  `json:"loss_detected"`
	Recovered    int64  `json:"recovered"`
	Unrecovered  int64  `json:"unrecovered"`
	ExtendedMax  uint64 `json:"extended_max"`
}

type trackState struct {
	stats   TrackStats
	base    uint64
	maxExt  uint64
	started bool
	seen    map[uint64]struct{}
	missing map[uint64]struct{}
}

// Tracker keeps independent sequence state for every source/track pair.
type Tracker struct {
	tracks map[string]*trackState
}

func NewTracker() *Tracker {
	return &Tracker{tracks: make(map[string]*trackState)}
}

func trackKey(e Event) string {
	return e.Source + "\x00" + e.Track
}

// Observe processes one event. Duplicates increment Duplicates but do not add
// a second member to the unique received set.
func (t *Tracker) Observe(e Event) (Observation, error) {
	if e.Source == "" || e.Track == "" {
		return Observation{}, internal.NewFault("rtp.normalize", e.Source, 0, "source and track are required")
	}
	key := trackKey(e)
	st := t.tracks[key]
	if st == nil {
		st = &trackState{
			seen:    make(map[uint64]struct{}),
			missing: make(map[uint64]struct{}),
		}
		st.stats.Source = e.Source
		st.stats.Track = e.Track
		t.tracks[key] = st
	}

	obs := Observation{Event: e, TrackKey: key}
	st.stats.Received++

	if !st.started {
		st.started = true
		st.base = uint64(e.Seq)
		st.maxExt = st.base
		obs.ExtendedSeq = st.base
		obs.Disposition = DispositionFirst
		st.seen[st.base] = struct{}{}
		st.stats.Unique++
		st.stats.ExtendedMax = st.maxExt
		return obs, nil
	}

	ext := extendSequence(e.Seq, st.maxExt)
	obs.ExtendedSeq = ext

	if _, ok := st.seen[ext]; ok {
		st.stats.Duplicates++
		obs.Disposition = DispositionDuplicate
		return obs, nil
	}

	if _, gap := st.missing[ext]; gap {
		delete(st.missing, ext)
		st.seen[ext] = struct{}{}
		st.stats.Unique++
		st.stats.Recovered++
		st.stats.OutOfOrder++
		st.stats.Unrecovered = int64(len(st.missing))
		obs.Disposition = DispositionRecovered
		return obs, nil
	}

	if ext <= st.maxExt {
		// An old sequence that was not explicitly tracked as a gap.
		st.seen[ext] = struct{}{}
		st.stats.Unique++
		st.stats.OutOfOrder++
		obs.Disposition = DispositionOutOfOrder
		return obs, nil
	}

	gapCount := int64(0)
	var missing []uint64
	if ext > st.maxExt+1 {
		for s := st.maxExt + 1; s < ext; s++ {
			if _, known := st.seen[s]; known {
				continue
			}
			st.missing[s] = struct{}{}
			missing = append(missing, s)
			gapCount++
		}
	}

	st.maxExt = ext
	st.seen[ext] = struct{}{}
	st.stats.Unique++
	st.stats.LossDetected += gapCount
	st.stats.Unrecovered = int64(len(st.missing))
	st.stats.ExtendedMax = st.maxExt
	obs.Missing = missing
	obs.GapDetected = int(gapCount)
	if gapCount > 0 {
		obs.Disposition = DispositionGap
	} else {
		obs.Disposition = DispositionSequential
	}
	return obs, nil
}

// extendSequence maps a uint16 RTP sequence to the 64-bit window following max.
func extendSequence(seq uint16, maxExt uint64) uint64 {
	maxLow := uint16(maxExt)
	delta := int32(seq) - int32(maxLow)
	if delta < -32768 {
		delta += 65536
	} else if delta > 32767 {
		delta -= 65536
	}
	return uint64(int64(maxExt) + int64(delta))
}

// Stats returns a copy of all track stats.
func (t *Tracker) Stats() []TrackStats {
	out := make([]TrackStats, 0, len(t.tracks))
	for _, st := range t.tracks {
		s := st.stats
		s.Unrecovered = int64(len(st.missing))
		out = append(out, s)
	}
	return out
}

// Totals aggregates counters across tracks.
func (t *Tracker) Totals() TrackStats {
	var all TrackStats
	for _, s := range t.Stats() {
		all.Received += s.Received
		all.Unique += s.Unique
		all.Duplicates += s.Duplicates
		all.OutOfOrder += s.OutOfOrder
		all.LossDetected += s.LossDetected
		all.Recovered += s.Recovered
		all.Unrecovered += s.Unrecovered
	}
	return all
}

// DecodeJSONL reads one JSON event per line. Fault offsets are byte offsets.
func DecodeJSONL(r io.Reader, source string) ([]Event, error) {
	br := bufio.NewReader(r)
	var events []Event
	offset := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineStart := offset
			offset += len(line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var e Event
				if jerr := json.Unmarshal(trimmed, &e); jerr != nil {
					return nil, internal.WrapFault("rtp.jsonl", source, lineStart, jerr)
				}
				if verr := validateEvent(e); verr != nil {
					return nil, internal.WrapFault("rtp.jsonl", source, lineStart, verr)
				}
				events = append(events, e)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, internal.WrapFault("rtp.jsonl", source, offset, err)
		}
	}
	if len(events) == 0 {
		return nil, internal.NewFault("rtp.jsonl", source, 0, "no JSONL events")
	}
	return events, nil
}

func validateEvent(e Event) error {
	if e.Source == "" {
		return fmt.Errorf("source is required")
	}
	if e.Kind != "audio" && e.Kind != "video" && e.Kind != "data" {
		return fmt.Errorf("kind must be audio, video or data: %q", e.Kind)
	}
	if e.Track == "" {
		return fmt.Errorf("track is required")
	}
	if e.PTSNS < 0 || e.ArrivalNS < 0 {
		return fmt.Errorf("pts_ns and arrival_ns must be non-negative")
	}
	if len(e.Digest) != 64 {
		return fmt.Errorf("digest must be a lowercase SHA-256 hex string")
	}
	return nil
}

// ParsePacket uses pion/rtp to parse a raw RTP packet.
func ParsePacket(packet []byte) (*rtp.Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(packet); err != nil {
		return nil, internal.WrapFault("rtp.packet", "", 0, err)
	}
	return &p, nil
}
