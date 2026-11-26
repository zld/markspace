package publish

import (
	"os"

	"github.com/abema/go-mp4"
	"github.com/sunfish-shogi/bufseekio"

	"regress/internal/fault"
)

// SegmentMeta is the timing information parsed from one media segment.
type SegmentMeta struct {
	Path          string
	Sequence      uint32 // mfhd.sequence_number
	BaseTime      uint64 // tfdt.baseMediaDecodeTime in track timescale
	SampleCount   uint32
	DurationTicks uint64 // sum of trun sample durations
	Timescale     uint32 // mdhd timescale from the init segment
}

// InitMeta describes the initialization segment.
type InitMeta struct {
	Timescale uint32
	TrackID   uint32
}

// ParseInit reads the track timescale from an ftyp+moov init segment using
// go-mp4 rather than any producer-side value.
func ParseInit(initPath string) (*InitMeta, error) {
	f, err := os.Open(initPath)
	if err != nil {
		return nil, fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	defer f.Close()
	r := bufseekio.NewReadSeeker(f, 128*1024, 4)
	boxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd(),
	})
	if err != nil {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
			"cannot parse init mdhd: "+err.Error()).Wrap(err)
	}
	if len(boxes) == 0 {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
			"init segment missing mdhd")
	}
	mdhd := boxes[0].Payload.(*mp4.Mdhd)
	meta := &InitMeta{Timescale: mdhd.Timescale}

	tk, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd(),
	})
	if err == nil && len(tk) > 0 {
		meta.TrackID = tk[0].Payload.(*mp4.Tkhd).TrackID
	}
	return meta, nil
}

// ParseSegment re-parses one media segment and returns its sequence, base
// decode time and sample timing. It independently walks moof/traf via go-mp4.
func ParseSegment(segPath string) (*SegmentMeta, error) {
	f, err := os.Open(segPath)
	if err != nil {
		return nil, fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	defer f.Close()
	r := bufseekio.NewReadSeeker(f, 128*1024, 4)

	meta := &SegmentMeta{Path: segPath}
	mfhd, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoof(), mp4.BoxTypeMfhd(),
	})
	if err != nil || len(mfhd) == 0 {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
			"segment missing mfhd: "+segPath).Wrap(err)
	}
	meta.Sequence = mfhd[0].Payload.(*mp4.Mfhd).SequenceNumber

	tfdt, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt(),
	})
	if err != nil || len(tfdt) == 0 {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
			"segment missing tfdt: "+segPath).Wrap(err)
	}
	meta.BaseTime = tfdt[0].Payload.(*mp4.Tfdt).GetBaseMediaDecodeTime()

	// Default sample duration lives in tfhd when trun omits per-sample
	// durations (ffmpeg emits trun flags 0x000205 with no duration field).
	var defaultDuration uint32
	tfhds, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfhd(),
	})
	if err == nil && len(tfhds) > 0 {
		tfhd := tfhds[0].Payload.(*mp4.Tfhd)
		if tfhd.GetFlags()&mp4.TfhdDefaultSampleDurationPresent != 0 {
			defaultDuration = tfhd.DefaultSampleDuration
		}
	}

	truns, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun(),
	})
	if err != nil || len(truns) == 0 {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
			"segment missing trun: "+segPath).Wrap(err)
	}
	for _, tb := range truns {
		tr := tb.Payload.(*mp4.Trun)
		meta.SampleCount += tr.SampleCount
		flags := tr.GetFlags()
		if flags&0x100 != 0 { // per-sample-duration present
			for _, e := range tr.Entries {
				meta.DurationTicks += uint64(e.SampleDuration)
			}
		} else if defaultDuration > 0 {
			meta.DurationTicks += uint64(defaultDuration) * uint64(tr.SampleCount)
		}
	}
	return meta, nil
}
