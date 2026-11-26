package timeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Event kinds accepted in the JSONL input.
const (
	KindEvent   = "event"
	KindRTP     = "rtp"
	KindSSRCMap = "ssrc_map"
)

// Event is one client-side observation. Track is the Matroska track number
// for "event" records; RTP records carry an SSRC instead.
type Event struct {
	Kind      string `json:"kind"`
	AtNS      int64  `json:"at_ns"`
	Track     uint64 `json:"track,omitempty"`
	SSRC      uint32 `json:"ssrc,omitempty"`
	ClockRate uint32 `json:"clock_rate,omitempty"`
	Sequence  uint16 `json:"seq,omitempty"`
	RTPTS     uint32 `json:"rtp_ts,omitempty"`
	HexPacket string `json:"packet_hex,omitempty"`
	Label     string `json:"label,omitempty"`
	// Line is the 1-based source line, retained for failure reporting.
	Line int `json:"-"`
}

// GapKind classifies why a block could not be fully reconciled.
type GapKind string

const (
	GapNone        GapKind = ""
	GapNoEvent     GapKind = "no_matching_event"
	GapRegression  GapKind = "time_regression"
	GapRTPMismatch GapKind = "rtp_timestamp_gap"
)

// Mapping ties one indexed media block to an event and an output frame range.
type Mapping struct {
	TrackNo     uint64
	BlockTimeNS int64
	EventAtNS   int64
	EventLine   int
	TimeErrorNS int64
	Gap         GapKind
	Detail      string
	// OutputFrameStart/End are 48 kHz frame indices assigned downstream;
	// zero here means "not yet assigned".
	OutputFrameStart int64
	OutputFrameEnd   int64
}

// LoadEvents parses newline-delimited JSON. Blank lines are ignored; any
// malformed record returns an error carrying its line number.
func LoadEvents(r io.Reader) ([]Event, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []Event
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(bytesTrimSpace(raw)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("events.jsonl line %d: %w", line, err)
		}
		e.Line = line
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("events.jsonl: %w", err)
	}
	return out, nil
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}

// BlockView is the subset of an indexed block the aligner needs.
type BlockView struct {
	TrackNo uint64
	TimeNS  int64
}

// Align matches each block to the nearest same-track event. When no event
// exists, or the event order contradicts block order, the mapping is marked
// with a gap instead of fabricating a time. maxErrorNS bounds an acceptable
// correspondence; larger residuals still map but are flagged in Detail.
func Align(blocks []BlockView, events []Event, maxErrorNS int64) []Mapping {
	byTrack := make(map[uint64][]Event)
	for _, e := range events {
		if e.Kind == KindEvent {
			byTrack[e.Track] = append(byTrack[e.Track], e)
		}
	}
	for k := range byTrack {
		sort.Slice(byTrack[k], func(i, j int) bool {
			return byTrack[k][i].AtNS < byTrack[k][j].AtNS
		})
	}
	out := make([]Mapping, 0, len(blocks))
	lastBlockTime := make(map[uint64]int64)
	for _, b := range blocks {
		m := Mapping{TrackNo: b.TrackNo, BlockTimeNS: b.TimeNS, EventLine: -1}
		if prev, ok := lastBlockTime[b.TrackNo]; ok && b.TimeNS < prev {
			m.Gap = GapRegression
			m.Detail = fmt.Sprintf("block time %d precedes previous %d", b.TimeNS, prev)
			out = append(out, m)
			lastBlockTime[b.TrackNo] = b.TimeNS
			continue
		}
		lastBlockTime[b.TrackNo] = b.TimeNS
		cands := byTrack[b.TrackNo]
		idx := nearestEvent(cands, b.TimeNS)
		if idx < 0 {
			m.Gap = GapNoEvent
			m.Detail = fmt.Sprintf("no event for track %d", b.TrackNo)
			out = append(out, m)
			continue
		}
		ev := cands[idx]
		m.EventAtNS = ev.AtNS
		m.EventLine = ev.Line
		m.TimeErrorNS = b.TimeNS - ev.AtNS
		if Abs(m.TimeErrorNS, 0) > maxErrorNS {
			m.Detail = fmt.Sprintf("residual %dns exceeds tolerance %dns", m.TimeErrorNS, maxErrorNS)
		}
		out = append(out, m)
	}
	return out
}

// nearestEvent returns the index of the event closest to targetNS, or -1.
func nearestEvent(evs []Event, targetNS int64) int {
	if len(evs) == 0 {
		return -1
	}
	idx := sort.Search(len(evs), func(i int) bool {
		return evs[i].AtNS >= targetNS
	})
	if idx == 0 {
		return 0
	}
	if idx == len(evs) {
		return idx - 1
	}
	if Abs(evs[idx].AtNS, targetNS) < Abs(evs[idx-1].AtNS, targetNS) {
		return idx
	}
	return idx - 1
}

// MaxTimeError returns the largest absolute residual across mappings.
func MaxTimeError(ms []Mapping) int64 {
	var max int64
	for _, m := range ms {
		if m.Gap == GapNoEvent || m.Gap == GapRegression {
			continue
		}
		if d := Abs(m.TimeErrorNS, 0); d > max {
			max = d
		}
	}
	return max
}
