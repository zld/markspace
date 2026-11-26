package timebase

import (
	"fmt"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/model"
)

// ClockAnchor correlates one RTP clock value with a media timeline value.
type ClockAnchor struct {
	ID         string
	SourceID   string
	TrackID    uint32
	RTPStart   uint32
	RTPHz      uint32
	MediaStart model.Us
}

// RTPToUs converts a possibly wrapped 32-bit RTP timestamp to unified us.
// prevExtended tracks the last unwrapped timestamp for wrap expansion.
func (a ClockAnchor) RTPToUs(ts uint32, prevExtended int64) (model.Us, int64) {
	ext := extendV0(ts, prevExtended)
	if prevExtended == 0 {
		// First call is interpreted relative to the anchor's RTPStart.
		ext = int64(ts)
	}
	deltaRTP := ext - int64(a.RTPStart)
	deltaUs := UnitsToUs(deltaRTP, a.RTPHz)
	return a.MediaStart + deltaUs, ext
}

// SyncResult is the integer-us conversion and absolute synchronization error.
type SyncResult struct {
	AnchorID   string   `json:"anchor_id"`
	SourceID   string   `json:"source_id"`
	TrackID    uint32   `json:"track_id"`
	RTPUs      model.Us `json:"rtp_us"`
	MediaUs    model.Us `json:"media_us"`
	ExpectedUs model.Us `json:"expected_us"`
	ErrorUs    model.Us `json:"error_us"`
	AbsErrorUs model.Us `json:"abs_error_us"`
}

// AnchorFromSpec builds and verifies an event anchor. MediaRaw is in the
// track timescale and is converted to integer microseconds.
func AnchorFromSpec(spec cmaf.AnchorSpec) (ClockAnchor, SyncResult, error) {
	if spec.Timescale == 0 || spec.RTPClockRate == 0 {
		return ClockAnchor{}, SyncResult{}, fmt.Errorf("anchor %s has zero clock rate", spec.AnchorID)
	}
	mediaUs := UnitsToUs(spec.MediaRaw, spec.Timescale)
	a := ClockAnchor{
		ID: spec.AnchorID, SourceID: spec.SourceID, TrackID: spec.TrackID,
		RTPStart: spec.RTPTime, RTPHz: spec.RTPClockRate, MediaStart: mediaUs,
	}
	// At the anchor RTP value itself, conversion must equal mediaUs.
	rtpUs, _ := a.RTPToUs(spec.RTPTime, 0)
	expected := model.Us(spec.ExpectedUs)
	errUs := rtpUs - expected
	res := SyncResult{
		AnchorID: spec.AnchorID, SourceID: spec.SourceID, TrackID: spec.TrackID,
		RTPUs: rtpUs, MediaUs: mediaUs, ExpectedUs: expected,
		ErrorUs: errUs, AbsErrorUs: absUs(errUs),
	}
	return a, res, nil
}

// SyncErrorMS converts microseconds to fractional milliseconds for output.
func SyncErrorMS(us model.Us) float64 {
	return float64(us) / 1000.0
}

// RTPPointInput is the minimal sequence/timestamp data needed for mapping.
type RTPPointInput struct {
	Sequence  uint16
	Timestamp uint32
}

// MappedRTPPoint places one reordered/recovered RTP packet on the us timeline.
type MappedRTPPoint struct {
	Sequence     uint16   `json:"sequence"`
	RTPTimestamp uint32   `json:"rtp_timestamp"`
	StartUs      model.Us `json:"start_us"`
}

// MapRTPPoints extends 32-bit RTP timestamps and maps them through an anchor.
func (a ClockAnchor) MapRTPPoints(points []RTPPointInput) []MappedRTPPoint {
	out := make([]MappedRTPPoint, 0, len(points))
	var prevExt int64
	have := false
	for _, p := range points {
		var us model.Us
		var ext int64
		if !have {
			us, ext = a.RTPToUs(p.Timestamp, 0)
			have = true
		} else {
			us, ext = a.RTPToUs(p.Timestamp, prevExt)
		}
		prevExt = ext
		out = append(out, MappedRTPPoint{Sequence: p.Sequence, RTPTimestamp: p.Timestamp, StartUs: us})
	}
	return out
}

func absUs(v model.Us) model.Us {
	if v < 0 {
		return -v
	}
	return v
}
