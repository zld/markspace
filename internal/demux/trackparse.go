package demux

import (
	"errors"
	"io"

	"mediasync/internal/ebml"
)

// readTrackEntry parses one TrackEntry master element into a Track header,
// including codec id, codec private, and the nested audio/video properties.
func (d *Demuxer) readTrackEntry(el *ebml.Element) (*Track, error) {
	if err := d.r.Enter(el); err != nil {
		return nil, err
	}
	tr := &Track{}
	for {
		child, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return tr, d.r.Exit()
		}
		if err != nil {
			return nil, err
		}
		switch child.ID {
		case ebml.IDTrackNumber, ebml.IDTrackUID, ebml.IDTrackType,
			ebml.IDDefaultDuration, ebml.IDPixelWidth, ebml.IDPixelHeight, ebml.IDChannels:
			raw, err := d.r.ReadData(child)
			if err != nil {
				return nil, err
			}
			v, err := ebml.Uint(raw)
			if err != nil {
				return nil, err
			}
			switch child.ID {
			case ebml.IDTrackNumber:
				tr.Number = uint32(v)
			case ebml.IDTrackUID:
				tr.UID = v
			case ebml.IDTrackType:
				tr.Type = v
			case ebml.IDDefaultDuration:
				tr.DefaultDuration = int64(v)
			case ebml.IDPixelWidth:
				tr.VideoWidth = v
			case ebml.IDPixelHeight:
				tr.VideoHeight = v
			case ebml.IDChannels:
				tr.AudioChannels = v
			}
		case ebml.IDCodecID:
			raw, err := d.r.ReadData(child)
			if err != nil {
				return nil, err
			}
			tr.CodecID = string(raw)
		case ebml.IDCodecPrivate:
			raw, err := d.r.ReadData(child)
			if err != nil {
				return nil, err
			}
			tr.CodecPrivate = append([]byte(nil), raw...)
		case ebml.IDAudio, ebml.IDVideo:
			if err := d.r.Enter(child); err != nil {
				return nil, err
			}
			if err := d.readMediaProps(child.ID, tr); err != nil {
				return nil, err
			}
		default:
			if err := d.r.Skip(child); err != nil {
				return nil, err
			}
		}
	}
}

// readMediaProps parses the nested Audio/Video master, currently extracting
// the audio sampling frequency while skipping unrelated children.
func (d *Demuxer) readMediaProps(kind uint32, tr *Track) error {
	for {
		child, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return d.r.Exit()
		}
		if err != nil {
			return err
		}
		switch {
		case kind == ebml.IDAudio && child.ID == ebml.IDSamplingFreq:
			raw, err := d.r.ReadData(child)
			if err != nil {
				return err
			}
			f, err := ebml.Float(raw)
			if err != nil {
				return err
			}
			tr.AudioFreq = f
		default:
			if err := d.r.Skip(child); err != nil {
				return err
			}
		}
	}
}
