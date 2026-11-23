// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file interprets metadata leaves: document
// info, track entries and cluster timecodes.
package ebml

import (
	"math"

	"mediad/internal/merr"
)

// Run consumes events until EOF, calling OnBlock.
func (d *Demuxer) Run() (int, error) {
	for {
		ev, err := d.w.Next()
		if err != nil {
			return d.Count, err
		}
		if err := d.handle(ev); err != nil {
			return d.Count, err
		}
	}
}

func (d *Demuxer) leaf(ev Event) error {
	switch ev.ID {
	case IDDocType:
		d.info.DocType = trimNul(ev.Data)
	case IDTimecodeScale:
		v, err := uintValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		if v == 0 {
			return merr.New("demux.timecodescale", merr.CodeBadInput, ev.HeaderOffset,
				"TimecodeScale must be positive")
		}
		d.info.TimecodeScaleNs = int64(v)
	case IDDuration:
		f, err := floatValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		scale := d.info.TimecodeScaleNs
		if scale == 0 {
			scale = DefaultTimecodeScaleNs
		}
		d.info.DurationNs = int64(f * float64(scale))
	case IDTrackNumber:
		v, err := uintValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		d.curTrack.Number = v
	case IDTrackType:
		v, err := uintValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		d.curTrack.Type = v
	case IDCodecID:
		d.curTrack.CodecID = trimNul(ev.Data)
	case IDDefaultDur:
		v, err := uintValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		d.curTrack.DefaultDurNs = int64(v) // nanoseconds by definition
	case IDTimecode:
		if d.inCluster == 0 || d.inInfo > 0 {
			return nil
		}
		v, err := uintValue(ev.Data, ev.HeaderOffset)
		if err != nil {
			return err
		}
		if v > math.MaxUint32 {
			return merr.New("demux.timecode", merr.CodeBadInput, ev.HeaderOffset,
				"cluster timecode %d exceeds 32 bits", v)
		}
		d.clusterTC = uint32(v)
	case IDSimpleBlock, IDBlock:
		b, err := d.parseBlock(ev)
		if err != nil {
			return err
		}
		d.emit(b)
	}
	return nil
}

func (d *Demuxer) handle(ev Event) error {
	switch ev.Kind {
	case Start:
		return d.start(ev)
	case End:
		return d.end(ev)
	default:
		return d.leaf(ev)
	}
}
