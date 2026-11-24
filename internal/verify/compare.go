package verify

import (
	"bytes"
	"fmt"

	"mediarc/internal/demux"
	"mediarc/internal/model"
	"mediarc/internal/track"
)

type Comparison struct {
	Report
	Mismatches []string `json:"mismatches,omitempty"`
}

// CompareRemux demuxes the produced container and compares every mapped
// sample and retained audio/video property byte-for-byte/value-for-value.
func CompareRemux(in *model.Archive, outBytes []byte, plan *track.Plan) (*Comparison, error) {
	container, err := demux.Demux(outBytes)
	if err != nil {
		return nil, err
	}
	out := container.Archive
	cmp := &Comparison{}
	cmp.InputTracks = SummarizeTracks(in)
	cmp.OutputTracks = SummarizeTracks(out)
	cmp.InputSampleCount = len(in.Samples)
	cmp.OutputSampleCount = len(out.Samples)
	cmp.SampleHashesMatch = true
	cmp.PropertiesMatch = true

	if plan == nil {
		plan = &track.Plan{}
		for _, tr := range in.Tracks {
			plan.Mappings = append(plan.Mappings, track.Mapping{SourceID: tr.ID, TargetID: tr.ID})
		}
	}

	for _, m := range plan.Mappings {
		it := in.Track(m.SourceID)
		ot := out.Track(m.TargetID)
		if it == nil || ot == nil {
			cmp.PropertiesMatch = false
			cmp.Mismatches = append(cmp.Mismatches, fmt.Sprintf("track missing source=%d target=%d", m.SourceID, m.TargetID))
			continue
		}
		compareProperty(cmp, m.TargetID, "time_base", it.TimeBase, ot.TimeBase)
		compareProperty(cmp, m.TargetID, "sample_rate", it.SampleRate, ot.SampleRate)
		compareProperty(cmp, m.TargetID, "channels", it.Channels, ot.Channels)
		compareProperty(cmp, m.TargetID, "layout", it.Layout, ot.Layout)
		compareProperty(cmp, m.TargetID, "color", colorKey(it.Color), colorKey(ot.Color))

		is := in.SamplesFor(m.SourceID)
		os := out.SamplesFor(m.TargetID)
		if len(is) != len(os) {
			cmp.SampleHashesMatch = false
			cmp.Mismatches = append(cmp.Mismatches, fmt.Sprintf("track %d sample count %d != output %d", m.TargetID, len(is), len(os)))
			continue
		}
		for i := range is {
			if is[i].Index != os[i].Index || is[i].PTS != os[i].PTS || is[i].DTS != os[i].DTS ||
				is[i].Duration != os[i].Duration || !bytes.Equal(is[i].Payload, os[i].Payload) {
				cmp.SampleHashesMatch = false
				cmp.Mismatches = append(cmp.Mismatches, fmt.Sprintf("track %d sample %d differs", m.TargetID, i))
				break
			}
		}
	}
	if len(cmp.Mismatches) > 0 {
		if !cmp.PropertiesMatch {
			return cmp, model.NewError(model.StageVerify, model.CodeNonLossless, "property verification failed", nil)
		}
		return cmp, model.NewError(model.StageVerify, model.CodeNonLossless, "sample verification failed", nil)
	}
	return cmp, nil
}

func compareProperty(cmp *Comparison, trackID uint32, name string, a, b any) {
	if fmt.Sprint(a) != fmt.Sprint(b) {
		cmp.PropertiesMatch = false
		cmp.Mismatches = append(cmp.Mismatches, fmt.Sprintf("track %d %s: %v != %v", trackID, name, a, b))
	}
}

func colorKey(c *model.ColorMeta) string {
	if c == nil {
		return "<absent>"
	}
	return fmt.Sprintf("%d/%d/%d/%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}
