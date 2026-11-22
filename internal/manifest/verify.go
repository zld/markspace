package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"mediq/internal/segment"
	"mediq/internal/timeline"
)

// Unmarshal parses a manifest from JSON.
func Unmarshal(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
			"reject_manifest", "parse failure: "+err.Error())
	}
	if m.SchemaVersion != Version {
		return nil, timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
			"reject_manifest", fmt.Sprintf("schema version %d, want %d", m.SchemaVersion, Version))
	}
	return &m, nil
}

// VerifyResult collects the round-trip verification outcome.
type VerifyResult struct {
	TotalRows      int
	MatchedRows    int
	ConsistencyPct float64
	Errors         []*timeline.Error
}

// RoundTrip re-parses rendered bytes and checks that every segment row
// of the original manifest is reproduced exactly, in the same order.
// It also re-validates the structural invariants that the pipeline
// guarantees: monotonic time, non-overlapping offsets and shared
// boundary numbers across representations.
func RoundTrip(original *Manifest, rendered []byte) (*VerifyResult, error) {
	reread, err := Unmarshal(rendered)
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{}
	origRows := flatten(original)
	gotRows := flatten(reread)
	res.TotalRows = len(origRows)
	if len(origRows) != len(gotRows) {
		return nil, timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
			"reject_manifest", fmt.Sprintf("row count changed: %d -> %d", len(origRows), len(gotRows)))
	}
	for i := range origRows {
		if origRows[i] == gotRows[i] {
			res.MatchedRows++
			continue
		}
		res.Errors = append(res.Errors, timeline.New(timeline.StageVerify,
			timeline.KindInconsistent, origRows[i].StartOffset, "quarantine_manifest_row",
			fmt.Sprintf("row %d mismatch: %+v != %+v", i, origRows[i], gotRows[i])))
	}
	if res.TotalRows > 0 {
		res.ConsistencyPct = float64(res.MatchedRows) * 100 / float64(res.TotalRows)
	}
	if err := CheckInvariants(reread); err != nil {
		return res, err
	}
	return res, nil
}

// CheckInvariants validates monotonic timestamps, gap-free and
// non-overlapping byte ranges within a representation and identical
// boundary numbering across representations.
func CheckInvariants(m *Manifest) error {
	boundaries := -1
	for _, rep := range m.Reps {
		if len(rep.Segments) == 0 {
			return timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
				"reject_manifest", "representation "+rep.ID+" has no segments")
		}
		if boundaries == -1 {
			boundaries = len(rep.Segments)
		} else if len(rep.Segments) != boundaries {
			return timeline.New(timeline.StageVerify, timeline.KindSkewExceeded, -1,
				"reject_manifest",
				fmt.Sprintf("rep %s has %d rows, want %d shared boundaries", rep.ID, len(rep.Segments), boundaries))
		}
		var prevEndOff int64
		var prevStartPTS int64
		for i, s := range rep.Segments {
			if s.Boundary != i {
				return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
					s.StartOffset, "reject_manifest",
					fmt.Sprintf("rep %s boundary numbering gap at row %d", rep.ID, i))
			}
			if s.Length <= 0 {
				return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
					s.StartOffset, "reject_manifest", "non-positive segment length")
			}
			if s.EndPTS90k <= s.StartPTS90k {
				return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
					s.StartOffset, "reject_manifest", "non-monotonic segment time")
			}
			if i > 0 {
				if s.StartOffset != prevEndOff {
					return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
						s.StartOffset, "reject_manifest",
						fmt.Sprintf("rep %s offset overlap/gap at boundary %d", rep.ID, i))
				}
				if s.StartPTS90k <= prevStartPTS {
					return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
						s.StartOffset, "reject_manifest", "timestamps not strictly monotonic")
				}
			}
			prevEndOff = s.StartOffset + s.Length
			prevStartPTS = s.StartPTS90k
		}
	}
	return nil
}

// ComparePlansCrossChecks that manifest rows match the aligned plans
// that produced them, catching drift between planning and rendering.
func ComparePlans(m *Manifest, plans []*segment.Plan) error {
	byRep := make(map[string]*segment.Plan, len(plans))
	for _, p := range plans {
		byRep[p.RepID] = p
	}
	for _, rep := range m.Reps {
		p, ok := byRep[rep.ID]
		if !ok {
			return timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
				"reject_manifest", "manifest rep not in plans: "+rep.ID)
		}
		if len(rep.Segments) != len(p.Segments) {
			return timeline.New(timeline.StageVerify, timeline.KindInconsistent, -1,
				"reject_manifest", "segment count drift for rep "+rep.ID)
		}
		for i, row := range rep.Segments {
			s := p.Segments[i]
			if row.StartOffset != s.StartOffset || row.Length != s.Bytes ||
				row.StartPTS90k != int64(s.StartPTS) || row.EndPTS90k != int64(s.EndPTS) {
				return timeline.New(timeline.StageVerify, timeline.KindInconsistent,
					row.StartOffset, "reject_manifest",
					fmt.Sprintf("rep %s boundary %d drifted from plan", rep.ID, i))
			}
		}
	}
	return nil
}

func flatten(m *Manifest) []SegmentRecord {
	var out []SegmentRecord
	for _, rep := range m.Reps {
		out = append(out, rep.Segments...)
	}
	return out
}
