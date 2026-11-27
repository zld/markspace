// Package fixture generates the deterministic, self-contained media fixtures
// used by the regression harness: a multi-rendition MP4-like container plus
// an RTP transport capture and a ground-truth plan. No external files are
// referenced; every byte is derived from fixed seeds so reruns are identical.
package fixture

import (
	"fmt"

	"regress/internal/clock"
	"regress/internal/model"
)

// Fault selects which reproducible anomaly a generated fixture exhibits.
type Fault string

const (
	FaultNone          Fault = "none"
	FaultWrap          Fault = "wrap"           // aggressive 32-bit timestamp wrap
	FaultTruncate      Fault = "truncate"       // MP4 container cut mid-box
	FaultAfterArtifact Fault = "after-artifact" // exit between artifact and commit
	FaultEvents        Fault = "events"         // one event dropped, one duplicated
)

// Layout constants shared by the writer and the readers.
const (
	VideoHz        int64 = 90000
	AudioHz        int64 = 48000
	FrameW               = 16
	FrameH               = 12
	AudioFrames          = 480                             // 10 ms at 48 kHz per audio sample
	VideoFrameTick       = clock.Tick(clock.MasterHz / 30) // 30 fps -> 3000 ticks
)

// Rendition names and track ids.
const (
	RendHigh = "video-high"
	RendLow  = "video-low"
	AudioID  = "audio-main"
	MetaID3  = "meta-id3"
	MetaEmsg = "meta-emsg"
	MetaSEI  = "meta-sei"
	MetaSub  = "meta-subtitle"
	MetaChap = "meta-chapter"
)

// Spec is the fully deterministic description of one generated scenario.
type Spec struct {
	Fault       Fault
	VideoFrames int // samples per rendition
	Seed        uint32
}

// TrackSpec describes one logical track's id, kind and source timescale.
type TrackSpec struct {
	ID        string
	Kind      model.TrackKind
	Timescale int64
}

// AllTrackSpecs returns the fixed track layout of every generated fixture.
func AllTrackSpecs() []TrackSpec {
	return []TrackSpec{
		{RendHigh, model.KindVideo, VideoHz},
		{RendLow, model.KindVideo, VideoHz},
		{AudioID, model.KindAudio, AudioHz},
		{MetaID3, model.KindID3, VideoHz},
		{MetaEmsg, model.KindEmsg, VideoHz},
		{MetaSEI, model.KindSEI, VideoHz},
		{MetaSub, model.KindSubtitle, VideoHz},
		{MetaChap, model.KindChapter, VideoHz},
	}
}

// DefaultSpec is the acceptance scenario.
func DefaultSpec(fault Fault) Spec {
	return Spec{Fault: fault, VideoFrames: 24, Seed: 0x5151}
}

// Plan builds the ground-truth publishing plan for the spec. Metadata events
// are scheduled every 6 video frames with a constant publish delay so delay
// quantiles have real data. Loss/duplicate anomalies are opt-in via
// FaultEvents: the acceptance scenario (FaultNone) publishes every planned
// event exactly once, so its run reports missing=0 duplicate=0.
func Plan(s Spec) ([]model.PlanItem, error) {
	if s.VideoFrames < 12 {
		return nil, fmt.Errorf("fixture: need at least 12 video frames, got %d", s.VideoFrames)
	}
	const publishDelay = clock.Tick(1500) // 50 ms at 90 kHz
	anomalies := s.Fault == FaultEvents
	metaTracks := []struct {
		id   string
		kind model.TrackKind
	}{
		{MetaID3, model.KindID3},
		{MetaEmsg, model.KindEmsg},
		{MetaSEI, model.KindSEI},
		{MetaSub, model.KindSubtitle},
		{MetaChap, model.KindChapter},
	}
	var items []model.PlanItem
	seq := uint32(0)
	for frame := 0; frame+2 < s.VideoFrames; frame += 6 {
		tick := clock.Tick(frame) * VideoFrameTick
		for _, tr := range metaTracks {
			item := model.PlanItem{
				Key:          fmt.Sprintf("%s/evt-%03d", tr.id, seq),
				Track:        tr.id,
				Kind:         tr.kind,
				Seq:          seq,
				ExpectedTick: tick,
				DelayTicks:   publishDelay,
			}
			// Only the opt-in events fault drops/duplicates anything.
			if anomalies && tr.id == MetaID3 && frame == 0 {
				item.Drop = true
			}
			if anomalies && tr.id == MetaEmsg && frame == 6 {
				item.Duplicate = true
			}
			items = append(items, item)
			seq++
		}
	}
	return items, nil
}
