package analyze

import (
	"fmt"
	"math"
	"sort"

	"assetcheck/internal/ebml"
	"assetcheck/internal/media"
)

const (
	Version       = "analyzer-v1"
	MaxPSNRDB     = 120.0
	PCMInt16Bytes = 2
)

// ReferenceSample is the deterministic PCM reference signal shared by the
// fixture generator and the readback/PSNR check. It is a 200 Hz sine-like
// integer pattern expressed without floating-point sample generation.
func ReferenceSample(sampleIndex int64) int16 {
	const period = 240 // 48 kHz / 240 = 200 Hz
	phase := sampleIndex % period
	if phase < 0 {
		phase += period
	}
	// One full signed triangular cycle, amplitude 30000.
	if phase < period/4 {
		return int16(-30000 + int64(240000*phase/period))
	}
	if phase < 3*period/4 {
		return int16(30000 - int64(240000*(phase-period/4)/(period/2)))
	}
	return int16(-30000 + int64(240000*(period-phase)/period))
}

type TrackReport struct {
	TrackID      int             `json:"track_id"`
	Language     string          `json:"language,omitempty"`
	Kind         media.EventKind `json:"kind"`
	CodecID      string          `json:"codec_id,omitempty"`
	Blocks       int             `json:"blocks"`
	PayloadBytes int64           `json:"payload_bytes"`
	FirstStart   media.Rational  `json:"first_start"`
	LastEnd      media.Rational  `json:"last_end"`
	DurationSec  float64         `json:"duration_sec"`
	TargetBPS    int64           `json:"target_bps,omitempty"`
	ActualBPS    float64         `json:"actual_bps,omitempty"`
	DeltaPct     float64         `json:"delta_pct,omitempty"`
	PSNRDB       float64         `json:"psnr_db,omitempty"`
	AlignMaxMS   float64         `json:"align_max_ms"`
}

type CueRecord struct {
	TrackID  int            `json:"track_id"`
	Language string         `json:"language"`
	CueID    string         `json:"cue_id"`
	Start    media.Rational `json:"start"`
	End      media.Rational `json:"end"`
	Text     string         `json:"text,omitempty"`
}

type Report struct {
	AnalyzerVersion string        `json:"analyzer_version"`
	TargetBPS       int64         `json:"target_bps"`
	ActualBPS       int64         `json:"actual_bps"`
	DeltaPct        float64       `json:"delta_pct"`
	PSNRDB          float64       `json:"psnr_db"`
	AlignMaxMS      float64       `json:"align_max_ms"`
	Tracks          []TrackReport `json:"tracks"`
	Cues            []CueRecord   `json:"cues"`
}

type trackState struct {
	info         ebml.TrackInfo
	report       TrackReport
	samplePos    int64
	sampleCount  int64
	errorEnergy  float64
	hasFirst     bool
	expectedNext media.Rational
}

type Collector struct {
	assetID string
	tracks  map[int]*trackState
	order   []int
	cues    []CueRecord
}

func NewCollector(assetID string, doc *ebml.Document) *Collector {
	c := &Collector{assetID: assetID, tracks: map[int]*trackState{}}
	if doc != nil {
		for _, t := range doc.Tracks {
			id := int(t.Number)
			c.tracks[id] = &trackState{info: t, report: TrackReport{
				TrackID:  id,
				Language: t.Language,
				CodecID:  t.CodecID,
			}}
			c.order = append(c.order, id)
		}
		sort.Ints(c.order)
	}
	return c
}

func (c *Collector) HandleEvent(ev media.TimeEvent) error {
	st := c.tracks[ev.TrackID]
	if st == nil {
		return media.Corrupt("ANALYZE_UNKNOWN_TRACK", "event has no track metadata").
			WithTrack(ev.TrackID, ev.BlockIndex).WithElement("", ev.Offset)
	}
	st.report.Blocks++
	st.report.PayloadBytes += int64(len(ev.Payload))
	if !st.hasFirst {
		st.hasFirst = true
		st.report.FirstStart = ev.Start
		st.expectedNext = ev.Start
	} else {
		drift := media.Abs(media.Sub(ev.Start, st.expectedNext))
		if ms := media.ToMilliseconds(drift); ms > st.report.AlignMaxMS {
			st.report.AlignMaxMS = ms
		}
	}
	st.report.LastEnd = ev.End

	switch ev.Kind {
	case media.EventAudio:
		st.report.Kind = media.EventAudio
		if err := c.checkPCM(st, ev); err != nil {
			return err
		}
		duration := media.Sub(ev.End, ev.Start)
		st.expectedNext = ev.End
		_ = duration
	case media.EventSubtitle:
		st.report.Kind = media.EventSubtitle
		c.cues = append(c.cues, CueRecord{
			TrackID: ev.TrackID, Language: st.info.Language, CueID: ev.CueID,
			Start: ev.Start, End: ev.End, Text: ev.Text,
		})
	case media.EventEffect:
		st.report.Kind = media.EventEffect
	}
	return nil
}

func (c *Collector) checkPCM(st *trackState, ev media.TimeEvent) error {
	if len(ev.Payload)%PCMInt16Bytes != 0 {
		return media.Corrupt("PCM_ALIGNMENT_INVALID", "PCM payload is not whole 16-bit samples").
			WithTrack(ev.TrackID, ev.BlockIndex).WithElement("", ev.Offset).
			WithDetail(fmt.Sprintf("bytes=%d", len(ev.Payload)))
	}
	for i := 0; i < len(ev.Payload); i += 2 {
		got := int16(uint16(ev.Payload[i]) | uint16(ev.Payload[i+1])<<8)
		want := ReferenceSample(st.samplePos)
		d := float64(got) - float64(want)
		st.errorEnergy += d * d
		st.samplePos++
		st.sampleCount++
	}
	return nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (c *Collector) Report() (*Report, error) {
	reports := make([]TrackReport, 0, len(c.order))
	var primary *TrackReport
	worstPSNR := MaxPSNRDB
	worstAlign := 0.0

	for _, id := range c.order {
		st := c.tracks[id]
		r := st.report
		if st.hasFirst {
			r.DurationSec = media.ToMilliseconds(media.Sub(r.LastEnd, r.FirstStart)) / 1000
		}
		if st.info.Type == ebml.TrackTypeAudio {
			rate := int64(math.Round(st.info.SamplingFrequency))
			r.TargetBPS = rate * int64(st.info.Channels) * int64(st.info.BitsPerSample)
			if r.DurationSec > 0 {
				r.ActualBPS = float64(r.PayloadBytes*8) / r.DurationSec
			}
			if r.TargetBPS > 0 {
				r.DeltaPct = round2((r.ActualBPS - float64(r.TargetBPS)) / float64(r.TargetBPS) * 100)
			}
			if st.sampleCount > 0 {
				if st.errorEnergy == 0 {
					r.PSNRDB = MaxPSNRDB
				} else {
					mse := st.errorEnergy / float64(st.sampleCount)
					r.PSNRDB = round2(20 * math.Log10(32767/math.Sqrt(mse)))
				}
				if r.PSNRDB < worstPSNR {
					worstPSNR = r.PSNRDB
				}
			}
			if primary == nil || id < primary.TrackID {
				cp := r
				primary = &cp
			}
		}
		if r.AlignMaxMS > worstAlign {
			worstAlign = r.AlignMaxMS
		}
		reports = append(reports, r)
	}

	c.alignCueLanguages(reports)
	for i := range reports {
		if reports[i].AlignMaxMS > worstAlign {
			worstAlign = reports[i].AlignMaxMS
		}
	}

	rep := &Report{
		AnalyzerVersion: Version,
		PSNRDB:          worstPSNR,
		AlignMaxMS:      round2(worstAlign),
		Tracks:          reports,
		Cues:            append([]CueRecord(nil), c.cues...),
	}
	if primary != nil {
		rep.TargetBPS = primary.TargetBPS
		rep.ActualBPS = int64(math.Round(primary.ActualBPS))
		rep.DeltaPct = primary.DeltaPct
	}
	// Exact fixtures should print the conventional perfect-score cap.
	if rep.PSNRDB == 0 && anyAudio(reports) {
		rep.PSNRDB = MaxPSNRDB
	}
	rep.PSNRDB = round2(rep.PSNRDB)
	return rep, nil
}

func anyAudio(rs []TrackReport) bool {
	for _, r := range rs {
		if r.Kind == media.EventAudio {
			return true
		}
	}
	return false
}

// alignCueLanguages compares equal cue IDs across language tracks. Generated
// translations share identical boundaries, so the clean fixture reports zero.
func (c *Collector) alignCueLanguages(reports []TrackReport) {
	byID := map[string][]CueRecord{}
	for _, cue := range c.cues {
		byID[cue.CueID] = append(byID[cue.CueID], cue)
	}
	byTrack := map[int]*TrackReport{}
	for i := range reports {
		byTrack[reports[i].TrackID] = &reports[i]
	}
	for _, group := range byID {
		if len(group) < 2 {
			continue
		}
		base := group[0]
		for _, cue := range group[1:] {
			startErr := media.ToMilliseconds(media.Abs(media.Sub(cue.Start, base.Start)))
			endErr := media.ToMilliseconds(media.Abs(media.Sub(cue.End, base.End)))
			v := math.Max(startErr, endErr)
			if tr := byTrack[cue.TrackID]; tr != nil && v > tr.AlignMaxMS {
				tr.AlignMaxMS = v
			}
			if tr := byTrack[base.TrackID]; tr != nil && v > tr.AlignMaxMS {
				tr.AlignMaxMS = v
			}
		}
	}
}
