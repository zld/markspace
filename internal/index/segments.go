package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// SampleData resolves the bytes of a sample by its index in the source track.
// Returning false means the payload is not available (for example a damaged
// fragment); the segment is still hashed, with the absence recorded.
type SampleData func(index int) ([]byte, bool)

// SegmentIndex is one segment's boundary, sample span and content hash.
type SegmentIndex struct {
	Index       int           `json:"index"`
	Track       model.TrackID `json:"track"`
	StartTick   int64         `json:"start_tick"`
	EndTick     int64         `json:"end_tick"`
	FirstSample int           `json:"first_sample"`
	LastSample  int           `json:"last_sample"`
	SampleCount int           `json:"sample_count"`
	Missing     int           `json:"missing_payloads"`
	Hash        string        `json:"hash"`
	EventIDs    []string      `json:"event_ids"`
	// Partial is set when at least one sample payload is missing; the hash is
	// still defined and recomputable, which lets a damaged layer continue.
	Partial bool `json:"partial"`
}

// BuildSegments computes per-segment hashes. The hash is a deterministic
// function of the segment's boundary, and for each covering sample its index,
// start tick, size, and payload digest. Re-running on the same inputs always
// yields the same hex digest.
func BuildSegments(track model.TrackID, timescale uint32, samples []model.Sample, data SampleData, segments []Segment, events []model.Event) ([]SegmentIndex, error) {
	if timescale == 0 {
		return nil, model.NewError(track, model.StageIndex, model.CodeInvalidParameter, false, "segment timescale must be positive")
	}
	if len(segments) == 0 {
		return nil, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false, "segment plan is empty")
	}
	var out []SegmentIndex
	cursor := 0
	for _, seg := range segments {
		if seg.EndTick <= seg.StartTick {
			return nil, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false,
				"segment %d has non-positive length [%d,%d)", seg.Index, seg.StartTick, seg.EndTick)
		}
		si := SegmentIndex{Index: seg.Index, Track: track, StartTick: seg.StartTick, EndTick: seg.EndTick,
			FirstSample: -1, LastSample: -1}
		h := sha256.New()
		writeU64(h, uint64(seg.Index))
		writeU64(h, uint64(seg.StartTick))
		writeU64(h, uint64(seg.EndTick))
		for cursor < len(samples) {
			t, err := clock.FromModel(samples[cursor].Time.RepairedTime)
			if err != nil {
				return nil, err
			}
			tk, err := t.FloorTicks(timescale)
			if err != nil {
				return nil, err
			}
			if tk >= seg.EndTick {
				break
			}
			if tk < seg.StartTick {
				// A sample before the segment start cannot belong to it; the
				// segment plan is validated by fan-in, so reaching here with a
				// non-empty segment is an ordering fault.
				return nil, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false,
					"sample %d at tick %d precedes segment %d start %d", cursor, tk, seg.Index, seg.StartTick)
			}
			if si.FirstSample < 0 {
				si.FirstSample = cursor
			}
			si.LastSample = cursor
			si.SampleCount++
			writeU64(h, uint64(cursor))
			writeU64(h, uint64(tk))
			writeU64(h, uint64(samples[cursor].Size))
			if data != nil {
				if payload, ok := data(cursor); ok {
					sum := sha256.Sum256(payload)
					h.Write([]byte{1})
					h.Write(sum[:])
				} else {
					h.Write([]byte{0})
					si.Missing++
					si.Partial = true
				}
			}
			cursor++
		}
		for _, ev := range events {
			et, err := clock.FromModel(ev.Time.RepairedTime)
			if err != nil {
				continue
			}
			etk, err := et.FloorTicks(timescale)
			if err != nil {
				continue
			}
			if etk >= seg.StartTick && etk < seg.EndTick {
				si.EventIDs = append(si.EventIDs, string(ev.Kind)+":"+ev.ID)
				writeU64(h, uint64(etk))
				h.Write([]byte(ev.Kind))
				h.Write([]byte{0})
				h.Write([]byte(ev.ID))
			}
		}
		si.Hash = hex.EncodeToString(h.Sum(nil))
		out = append(out, si)
	}
	return out, nil
}

func writeU64(w interface{ Write([]byte) (int, error) }, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	w.Write(b[:])
}

// VerifySegments recomputes every segment hash and reports how many match the
// previously produced values. Any mismatch means the index is not
// reproducible and must not be trusted.
func VerifySegments(track model.TrackID, timescale uint32, samples []model.Sample, data SampleData, planned []Segment, existing []SegmentIndex, events []model.Event) (matched, total int, err error) {
	rebuilt, err := BuildSegments(track, timescale, samples, data, planned, events)
	if err != nil {
		return 0, 0, err
	}
	if len(rebuilt) != len(existing) {
		return 0, len(existing), fmt.Errorf("segment count changed: rebuilt %d, indexed %d", len(rebuilt), len(existing))
	}
	for i := range existing {
		if rebuilt[i].Hash == existing[i].Hash {
			matched++
		}
	}
	return matched, len(existing), nil
}

// Segment is re-exported from fanin without importing it, keeping the index
// package free of orchestration dependencies.
type Segment struct {
	Index     int
	StartTick int64
	EndTick   int64
}
