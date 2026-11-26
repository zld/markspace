package cmaf

import (
	"errors"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"replay/internal/model"
)

// trafState accumulates one traf box while walking a fragment.
type trafState struct {
	trackID    uint32
	defaultDur uint32
	tfdt       uint64
	hasTfdt    bool
	dur        uint64
	samples    uint64
}

// ParseFragment parses one media fragment (moof+mdat). A fragment whose
// boxes are truncated is reported with its file offset and returned as a
// damaged Fragment when its moof was still readable, or as nil when the
// moof itself is cut; parsing of that fragment stops at the bad box.
func (p *Parser) ParseFragment(path string, init *Init) (*Fragment, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size := fileSize(f)
	frag := &Fragment{Path: path, FileSize: size}
	var trafs []*trafState
	var cur *trafState
	badNesting := false
	noteNesting := func(box string) {
		if badNesting {
			return
		}
		badNesting = true
		p.Report.Addf(model.CodeMalformedBox, model.SevWarn, path, -1,
			"%s box outside traf; ignored", box)
	}
	walkErr := walk(f, 0, size, 0, func(bi *mp4.BoxInfo, _ int) error {
		switch bi.Type {
		case mp4.BoxTypeMfhd():
			var b mp4.Mfhd
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			frag.Seq = b.SequenceNumber
		case mp4.BoxTypeTraf():
			cur = &trafState{}
			trafs = append(trafs, cur)
		case mp4.BoxTypeTfhd():
			if cur == nil {
				noteNesting("tfhd")
				return nil
			}
			var b mp4.Tfhd
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			cur.trackID = b.TrackID
			cur.defaultDur = b.DefaultSampleDuration
		case mp4.BoxTypeTfdt():
			if cur == nil {
				noteNesting("tfdt")
				return nil
			}
			var b mp4.Tfdt
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			if b.Version == 1 {
				cur.tfdt = b.BaseMediaDecodeTimeV1
			} else {
				cur.tfdt = uint64(b.BaseMediaDecodeTimeV0)
			}
			cur.hasTfdt = true
		case mp4.BoxTypeTrun():
			if cur == nil {
				noteNesting("trun")
				return nil
			}
			var b mp4.Trun
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			if b.SampleCount > MaxTrunSamples {
				return fmt.Errorf("cmaf: %s: trun declares %d samples (limit %d)",
					path, b.SampleCount, MaxTrunSamples)
			}
			for _, e := range b.Entries {
				d := e.SampleDuration
				if d == 0 {
					d = cur.defaultDur
				}
				cur.dur += uint64(d)
				cur.samples++
			}
		}
		return nil
	})
	if walkErr != nil {
		var te *TruncError
		if errors.As(walkErr, &te) {
			p.Report.Addf(model.CodeTruncatedBox, model.SevWarn, path, te.Offset, "%s", te)
			if len(trafs) == 0 {
				return nil, nil // moof unreadable: nothing recoverable
			}
			frag.Damaged = true
		} else {
			return nil, walkErr
		}
	}
	for _, t := range trafs {
		switch init.TrackTypeOf(t.trackID) {
		case TrackVideo:
			frag.Tfdt, frag.VideoDur, frag.HasVideo = t.tfdt, t.dur, t.hasTfdt
		case TrackAudio:
			frag.AudioTfdt, frag.AudioDur, frag.HasAudio = t.tfdt, t.dur, t.hasTfdt
		}
	}
	return frag, nil
}
