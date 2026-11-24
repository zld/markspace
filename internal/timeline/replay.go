package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"mediarc/internal/model"
)

// Event is the unified timeline representation for demuxed metadata and
// recovered RTP/media packets.
type Event struct {
	ID           string
	TrackID      uint32
	PTS          model.Timestamp
	Kind         string
	Payload      []byte
	SourceOffset int64
	ArrivalSeq   int
	RTPSequence  uint16
	Recovered    bool
}

type Revision struct {
	Rewind       int      `json:"rewind"`
	InsertedID   string   `json:"inserted_id"`
	OrderedIDs   []string `json:"ordered_ids"`
	CommittedIDs []string `json:"committed_ids"`
	Hash         string   `json:"hash"`
	WatermarkMS  int64    `json:"watermark_ms"`
}

type Result struct {
	OrderedEvents []Event    `json:"-"`
	OrderedIDs    []string   `json:"ordered_ids"`
	CommittedIDs  []string   `json:"committed_ids"`
	Revisions     []Revision `json:"revisions"`
	RewindCount   int        `json:"rewind_count"`
	OrderHash     string     `json:"order_hash"`
	WatermarkMS   int64      `json:"watermark_ms"`
}

type Engine struct {
	AllowedLatenessMS int64
	HardBoundaryMS    int64
	events            []Event
	seen              map[string]struct{}
	arrival           int
	wm                *Watermark
	result            Result
}

func NewEngine(allowedLatenessMS, hardBoundaryMS int64) *Engine {
	if hardBoundaryMS < allowedLatenessMS {
		hardBoundaryMS = allowedLatenessMS
	}
	return &Engine{
		AllowedLatenessMS: allowedLatenessMS,
		HardBoundaryMS:    hardBoundaryMS,
		seen:              map[string]struct{}{},
		wm:                NewWatermark(allowedLatenessMS),
	}
}

func (e *Engine) Result() Result { return e.result }

// Add inserts one event. Out-of-order events inside the hard boundary cause a
// deterministic full recomputation. Events beyond the boundary return a
// structured timeline error and do not alter the accepted result.
func (e *Engine) Add(ev Event) (*Result, error) {
	if ev.ID == "" {
		return nil, model.NewError(model.StageTimeline, model.CodeInvalidArgument, "event without ID", nil)
	}
	if _, dup := e.seen[ev.ID]; dup {
		return &e.result, nil // duplicate RTP/event delivery is idempotent
	}
	e.arrival++
	if ev.ArrivalSeq == 0 {
		ev.ArrivalSeq = e.arrival
	}

	if max, ok := e.wm.MaxPTS(); ok && e.wm.IsBeyondLateness(ev.PTS, e.HardBoundaryMS) {
		gap := e.wm.LateGapMS(ev.PTS)
		err := model.NewError(model.StageTimeline, model.CodeLateBoundary,
			fmt.Sprintf("event %s is %dms behind max %dms; hard boundary is %dms",
				ev.ID, gap, max.Millis(), e.HardBoundaryMS), nil)
		return &e.result, model.AtTimestamp(model.AtTrack(model.AtOffset(err, ev.SourceOffset), ev.TrackID), ev.PTS)
	}

	_, late := e.wm.MaxPTS()
	isRewind := late && ev.PTS.Before(mustMax(e.wm))
	e.events = append(e.events, ev)
	e.seen[ev.ID] = struct{}{}
	e.wm.Observe(ev.PTS)

	ordered := SortEvents(e.events)
	ids := make([]string, 0, len(ordered))
	for _, x := range ordered {
		ids = append(ids, x.ID)
	}
	hash := hashOrder(ordered)
	wmMS, _ := e.wm.Horizon()
	committed := commitUntilHorizon(ordered, wmMS)

	if isRewind {
		e.result.RewindCount++
		rev := Revision{
			Rewind:       e.result.RewindCount,
			InsertedID:   ev.ID,
			OrderedIDs:   append([]string(nil), ids...),
			CommittedIDs: append([]string(nil), committed...),
			Hash:         hash,
			WatermarkMS:  wmMS.Millis(),
		}
		e.result.Revisions = append(e.result.Revisions, rev)
	}
	e.result.OrderedEvents = ordered
	e.result.OrderedIDs = ids
	e.result.CommittedIDs = committed
	e.result.OrderHash = hash
	e.result.WatermarkMS = wmMS.Millis()
	cp := e.result
	return &cp, nil
}

func mustMax(w *Watermark) model.Timestamp {
	ts, _ := w.MaxPTS()
	return ts
}

func commitUntilHorizon(events []Event, horizon model.Timestamp) []string {
	var ids []string
	for _, ev := range events {
		if !ev.PTS.After(horizon) {
			ids = append(ids, ev.ID)
		}
	}
	return ids
}

func hashOrder(events []Event) string {
	h := sha256.New()
	for _, ev := range events {
		pb := sha256.Sum256(ev.Payload)
		fmt.Fprintf(h, "%s|%d|%d/%d|%d|%x\n", ev.ID, ev.TrackID,
			ev.PTS.Base.Num, ev.PTS.Base.Den, ev.PTS.Ticks, pb[:8])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// MetadataEvents converts parsed timed metadata into timeline events.
func MetadataEvents(in []model.TimedEvent) []Event {
	out := make([]Event, 0, len(in))
	for _, ev := range in {
		out = append(out, Event{
			ID: ev.ID, TrackID: ev.TrackID, PTS: ev.PTS, Kind: string(ev.Kind),
			Payload: ev.Payload, SourceOffset: ev.SourceOffset,
		})
	}
	return out
}
