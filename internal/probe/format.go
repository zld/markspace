package probe

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// Delivery targets used by the normalize policy. They are fixed constants so
// repeated runs of identical inputs always produce identical decisions.
const (
	TargetChannelCount uint16 = 2
	TargetSampleRate   uint32 = 48000
)

// attachFormat resolves the sample entry (avc1/mp4a), populates format
// metadata and records explicit conflict decisions instead of guessing.
func attachFormat(r io.ReadSeeker, trak *mp4.BoxInfo, t *model.Track, cfg ProbeConfig, errs *model.ErrorSet) {
	entry, colr, err := extractSampleEntry(r, trak)
	if err != nil {
		errs.Add(model.MediaError{
			Kind: model.KindTruncatedBox, TrackID: t.ID, SampleIndex: -1,
			Detail: "sample entry: " + err.Error(),
		})
		return
	}
	switch e := entry.(type) {
	case *mp4.VisualSampleEntry:
		vf := &model.VideoFormat{
			Codec:            e.GetType().String(),
			Width:            e.Width,
			Height:           e.Height,
			HasColorMetadata: colr != nil,
		}
		if colr != nil {
			vf.ColourType = string(colr.ColourType[:])
			vf.ColourPrimaries = colr.ColourPrimaries
			vf.Transfer = colr.TransferCharacteristics
			vf.Matrix = colr.MatrixCoefficients
			vf.FullRange = colr.FullRangeFlag
			detectColorConflict(vf, cfg, t.ID, errs)
		}
		t.Video = vf
	case *mp4.AudioSampleEntry:
		rate := e.SampleRate >> 16 // 16.16 fixed point
		af := &model.AudioFormat{
			Codec:         e.GetType().String(),
			ChannelCount:  e.ChannelCount,
			ChannelLayout: canonicalLayout(e.ChannelCount),
			SampleRate:    rate,
			SampleSize:    e.SampleSize,
		}
		if e.ChannelCount != TargetChannelCount {
			errs.Add(conflict(model.KindChannelLayoutConflict, t.ID, cfg.ChannelPolicy,
				fmt.Sprintf("channels=%d target=%d", e.ChannelCount, TargetChannelCount)))
		}
		if rate != TargetSampleRate {
			errs.Add(conflict(model.KindSampleRateConflict, t.ID, cfg.ChannelPolicy,
				fmt.Sprintf("sample_rate=%d target=%d", rate, TargetSampleRate)))
		}
		t.Audio = af
	}
}

// extractSampleEntry pulls the first visual/audio sample entry plus any colr
// box from the stsd subtree.
func extractSampleEntry(r io.ReadSeeker, trak *mp4.BoxInfo) (mp4.IBox, *mp4.Colr, error) {
	payloads, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1(), mp4.BoxTypeColr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a(), mp4.BoxTypeColr()},
	})
	if err != nil {
		return nil, nil, err
	}
	var entry mp4.IBox
	var colr *mp4.Colr
	for _, p := range payloads {
		switch box := p.Payload.(type) {
		case *mp4.VisualSampleEntry, *mp4.AudioSampleEntry:
			if entry == nil {
				entry = box
			}
		case *mp4.Colr:
			colr = box
		}
	}
	if entry == nil {
		return nil, nil, fmt.Errorf("no avc1/mp4a sample entry in stsd")
	}
	return entry, colr, nil
}

// conflict builds a policy-annotated MediaError.
func conflict(kind model.ErrorKind, trackID uint32, policy model.Policy, detail string) model.MediaError {
	return model.MediaError{
		Kind: kind, TrackID: trackID, SampleIndex: -1,
		Severity: model.SeverityWarning,
		Detail:   fmt.Sprintf("policy=%s %s", policy, detail),
	}
}

func canonicalLayout(channels uint16) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("unknown(%d)", channels)
	}
}

// detectColorConflict flags non-BT.709 nclx triples. The sanctioned delivery
// triple is 1/1/1 (BT.709); anything else is an explicit conflict.
func detectColorConflict(vf *model.VideoFormat, cfg ProbeConfig, trackID uint32, errs *model.ErrorSet) {
	if vf.ColourType != "nclx" {
		return
	}
	if vf.ColourPrimaries == 1 && vf.Transfer == 1 && vf.Matrix == 1 {
		return
	}
	errs.Add(conflict(model.KindColorConflict, trackID, cfg.ColorPolicy,
		fmt.Sprintf("nclx=%d/%d/%d target=1/1/1",
			vf.ColourPrimaries, vf.Transfer, vf.Matrix)))
}
