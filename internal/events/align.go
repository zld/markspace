package events

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"replay/internal/clock"
)

// Decision is the per-event verdict produced by the plan stage.
type Decision string

const (
	DecisionAccepted       Decision = "accepted"
	DecisionDuplicate      Decision = "duplicate"
	DecisionConflict       Decision = "conflict"
	DecisionSkipConfidence Decision = "skip_confidence"
	DecisionSkipBudget     Decision = "skip_budget"
	DecisionSkipUntrusted  Decision = "skip_untrusted"
	DecisionNoKeyframe     Decision = "no_keyframe"
	DecisionRTPBad         Decision = "rtp_bad"
)

// Event is one JSONL record. Kind "event" carries a highlight; kind
// "clock" carries a reference RTP packet that feeds wrap detection but
// never produces a clip.
type Event struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Confidence float64  `json:"confidence"`
	DurationS  float64  `json:"duration_s"`
	Packets    []string `json:"packets"` // base64 RTP packets
	Content    string   `json:"content"` // deduplication key beyond the ID
}

// AlignedEvent is an event mapped onto the media clock with its verdict.
type AlignedEvent struct {
	ID          string             `json:"id"`
	Kind        string             `json:"kind"`
	Confidence  float64            `json:"confidence"`
	Content     string             `json:"content"`
	MediaTime   clock.MediaTime    `json:"-"`
	TimeTick    int64              `json:"media_tick"`
	Timescale   uint32             `json:"timescale"`
	DurationS   float64            `json:"duration_s"`
	Decision    Decision           `json:"decision"`
	RejectWhy   string             `json:"reject_reason,omitempty"`
	DuplicateOf string             `json:"duplicate_of,omitempty"`
	RTPBad      int                `json:"rtp_bad_packets"`
	ContentHash [32]byte           `json:"-"`
}

// Feed is the parsed and ordered event feed plus RTP health counters.
type Feed struct {
	Events []*AlignedEvent
	Stats  Stats
	SHA    [32]byte
}

type indexedEvent struct {
	line  int
	event Event
}

// contentHash makes "same ID same content" deterministic and independent
// of field ordering in the JSON.
func contentHash(e Event) [32]byte {
	canonical, _ := json.Marshal(struct {
		ID      string  `json:"id"`
		Content string  `json:"content"`
		Dur     float64 `json:"duration_s"`
		Conf    float64 `json:"confidence"`
	}{e.ID, e.Content, e.DurationS, e.Confidence})
	return sha256.Sum256(canonical)
}

// parseLine decodes one JSONL record; empty lines and comments are skipped.
func parseLine(raw []byte) (*Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, clock.NewError(clock.KindRTP, "jsonl",
			fmt.Sprintf("invalid json: %v", err))
	}
	if e.Kind == "" {
		e.Kind = "event"
	}
	return &e, nil
}

// ingestPackets feeds every packet of a record through the RTP stream and
// returns the media time of the last valid packet (the event stamp).
func ingestPackets(s *Stream, e *Event) (clock.MediaTime, int, error) {
	var last clock.MediaTime
	bad := 0
	for _, enc := range e.Packets {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			s.Bad++
			bad++
			continue
		}
		mt, err := s.Ingest(raw)
		if err != nil {
			bad++
			continue
		}
		last = mt
	}
	if bad == len(e.Packets) {
		return last, bad, clock.NewError(clock.KindRTP, "rtp",
			"no valid packet in record").WithEvent(e.ID)
	}
	return last, bad, nil
}

// Load reads a JSONL feed, maps every packet onto the media clock and
// applies ordering, duplicate merging and conflict detection.
func Load(path string, clockRate uint32, anchor Anchor, minConfidence float64) (*Feed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	reader := io.TeeReader(f, h)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	stream, err := NewStream(clockRate, anchor)
	if err != nil {
		return nil, err
	}
	var records []indexedEvent
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		e, perr := parseLine(raw)
		if perr != nil {
			return nil, perr
		}
		if e == nil {
			continue
		}
		records = append(records, indexedEvent{line: lineNo, event: *e})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	aligned := make([]*AlignedEvent, 0, len(records))
	for _, rec := range records {
		e := rec.event
		mt, bad, ierr := ingestPackets(stream, &e)
		if e.Kind == "clock" {
			continue // reference packets only advance the RTP stream state
		}
		ae := &AlignedEvent{
			ID: e.ID, Kind: e.Kind, Confidence: e.Confidence,
			Content: e.Content, MediaTime: mt, TimeTick: mt.Ticks,
			Timescale: clockRate, DurationS: e.DurationS,
			RTPBad: bad, ContentHash: contentHash(e),
		}
		if ierr != nil {
			ae.Decision = DecisionRTPBad
			ae.RejectWhy = ierr.Error()
		} else if e.Confidence < minConfidence {
			ae.Decision = DecisionSkipConfidence
			ae.RejectWhy = fmt.Sprintf("confidence %.2f < %.2f", e.Confidence, minConfidence)
		}
		aligned = append(aligned, ae)
	}

	// Sorting by (time, id, content-hash) makes the manifest order and the
	// duplicate/conflict resolution fully deterministic.
	sort.SliceStable(aligned, func(i, j int) bool {
		if aligned[i].TimeTick != aligned[j].TimeTick {
			return aligned[i].TimeTick < aligned[j].TimeTick
		}
		if aligned[i].ID != aligned[j].ID {
			return aligned[i].ID < aligned[j].ID
		}
		return hex.EncodeToString(aligned[i].ContentHash[:]) <
			hex.EncodeToString(aligned[j].ContentHash[:])
	})

	// Same ID: identical content merges into the first occurrence; any
	// content difference is a hard conflict that blocks manifest commit.
	firstByID := map[string]*AlignedEvent{}
	var out []*AlignedEvent
	for _, ae := range aligned {
		first, seen := firstByID[ae.ID]
		if !seen {
			firstByID[ae.ID] = ae
			out = append(out, ae)
			continue
		}
		if ae.ContentHash == first.ContentHash {
			ae.Decision = DecisionDuplicate
			ae.DuplicateOf = first.ID
			out = append(out, ae)
			continue
		}
		return nil, clock.NewError(clock.KindConflict, "align",
			fmt.Sprintf("event %s appears with conflicting content", ae.ID)).WithEvent(ae.ID)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return &Feed{Events: out, Stats: stream.Report(), SHA: sum}, nil
}
