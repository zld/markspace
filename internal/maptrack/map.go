// Package maptrack validates explicit one-to-one source-to-output track rules.
package maptrack

import (
	"fmt"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
)

// Rule selects one source track. MP4/MOV rules set TrackID. RTP rules set
// RTP with both SSRC and payload type.
type Rule struct {
	Input       string              `json:"input"`
	TrackID     uint32              `json:"track_id,omitempty"`
	RTP         *demux.RTPSourceKey `json:"rtp,omitempty"`
	TargetID    uint32              `json:"target_id"`
	TargetLabel string              `json:"target_label,omitempty"`
	RequireTB   *clock.Timebase     `json:"require_timebase,omitempty"`
}

type MappedTrack struct {
	TargetID  uint32           `json:"target_id"`
	Label     string           `json:"label,omitempty"`
	SourcePos string           `json:"source"`
	Timebase  clock.Timebase   `json:"timebase"`
	Track     *demux.Track     `json:"-"`
	RTPStream *demux.RTPStream `json:"-"`
}

type Table struct {
	Tracks         []*MappedTrack `json:"tracks"`
	CommonTimebase clock.Timebase `json:"common_timebase"`
}

// Catalog is the set of probed/parsed source documents addressable by rules.
type Catalog struct {
	MP4 []*demux.Document
	RTP []*demux.RTPDocument
}

func (r Rule) Position() string {
	if r.RTP != nil {
		return fmt.Sprintf("%s#rtp:%08x/pt=%d", r.Input, r.RTP.SSRC, r.RTP.PayloadType)
	}
	return fmt.Sprintf("%s#track=%d", r.Input, r.TrackID)
}

// Apply resolves every rule and enforces unique sources and target IDs. The
// returned order is exactly the rule/output order requested by the caller.
func Apply(catalog Catalog, rules []Rule) (*Table, error) {
	if len(rules) == 0 {
		return nil, fault.AtPosition(fault.KindMapping, "", "at least one track mapping is required")
	}
	table := &Table{}
	seenSource := map[string]bool{}
	seenTarget := map[uint32]bool{}
	var bases []clock.Timebase

	for _, rule := range rules {
		pos := rule.Position()
		sourceKey := pos
		if seenSource[sourceKey] {
			return nil, fault.AtPosition(fault.KindMapping, pos, "duplicate source mapping")
		}
		seenSource[sourceKey] = true
		if rule.TargetID == 0 {
			return nil, fault.AtPosition(fault.KindMapping, pos, "target_id must be non-zero")
		}
		if seenTarget[rule.TargetID] {
			return nil, fault.AtPosition(fault.KindMapping, pos, "duplicate target track id %d", rule.TargetID)
		}
		seenTarget[rule.TargetID] = true

		mt, err := resolve(catalog, rule)
		if err != nil {
			return nil, err
		}
		if err := mt.Timebase.Validate(); err != nil {
			return nil, fault.AtPosition(fault.KindMapping, pos, "incompatible timebase: %v", err)
		}
		if rule.RequireTB != nil && !rule.RequireTB.Equal(mt.Timebase) {
			return nil, fault.AtPosition(fault.KindMapping, pos,
				"incompatible timebase: rule requires %s, source is %s", rule.RequireTB, mt.Timebase)
		}
		mt.Label = rule.TargetLabel
		table.Tracks = append(table.Tracks, mt)
		bases = append(bases, mt.Timebase)
	}
	common, err := clock.CommonTimebase(bases...)
	if err != nil {
		return nil, fault.AtPosition(fault.KindMapping, "", "incompatible timebases: %v", err)
	}
	table.CommonTimebase = common
	return table, nil
}

func resolve(c Catalog, r Rule) (*MappedTrack, error) {
	if r.RTP != nil {
		for _, doc := range c.RTP {
			if doc.Path != r.Input {
				continue
			}
			for _, st := range doc.Streams {
				if st.Key == *r.RTP {
					return &MappedTrack{
						TargetID: r.TargetID, SourcePos: r.Position(),
						Timebase: st.Timebase, RTPStream: st,
					}, nil
				}
			}
		}
		return nil, fault.AtPosition(fault.KindMapping, r.Position(), "RTP source not found")
	}

	for _, doc := range c.MP4 {
		if doc.Path != r.Input {
			continue
		}
		if tr := doc.Track(r.TrackID); tr != nil {
			return &MappedTrack{
				TargetID: r.TargetID, SourcePos: r.Position(),
				Timebase: tr.Timebase, Track: tr,
			}, nil
		}
	}
	return nil, fault.AtPosition(fault.KindMapping, r.Position(), "source track not found")
}

// Samples returns the valid ordered samples for a mapped source.
func (m *MappedTrack) Samples() []*demux.Sample {
	if m.Track != nil {
		return m.Track.Samples
	}
	if m.RTPStream != nil {
		return m.RTPStream.Samples
	}
	return nil
}

// SourceDuration returns the last sample end in its native timebase.
func (m *MappedTrack) SourceDuration() int64 {
	s := m.Samples()
	if len(s) == 0 {
		return 0
	}
	last := s[len(s)-1]
	return last.DTS + last.Duration
}
