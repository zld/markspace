// Package demux parses Matroska/WebM containers into raw, never-re-encoded
// block samples and binds them to output roles through an explicit mapping.
package demux

import (
	"errors"
	"fmt"
	"io"

	"mediasync/internal/ebml"
)

// MaxInputSize bounds a container the demuxer will accept. Streaming parsing
// keeps actual memory use far below this, but a corrupt header cannot declare
// an unbounded stream.
const MaxInputSize = 512 << 20 // 512 MiB

// Sample is one demuxed block. Payload is the untouched codec data; the
// pipeline copies bytes through without decoding them.
type Sample struct {
	Track     uint32
	Seq       uint64 // per-track monotonic sequence assigned in block order
	TimeTicks int64  // absolute cluster-relative ticks in TimecodeScale units
	Keyframe  bool
	Payload   []byte
}

// Summary captures independently checkable facts about the input container.
type Summary struct {
	TimecodeScaleNS int64
	Tracks          []TrackInfo
	TotalBlocks     int
	Bytes           int64
	Truncated       *ebml.Error
}

// TrackInfo is the per-track slice of the input summary.
type TrackInfo struct {
	Number  uint32
	Type    uint64
	CodecID string
	Blocks  int
}

// Demuxer streams one container. Construct with New, read Samples with Next.
type Demuxer struct {
	r         *ebml.Reader
	scaleNS   int64
	tracks    map[uint32]*Track
	order     []uint32
	clusters  []clusterFrame
	clusterTC int64
	seq       map[uint32]uint64
	bytes     int64
	summary   Summary
	ended     bool
}

type clusterFrame struct {
	end int64 // -1 for unknown-length clusters
}

// New parses the EBML header, Segment, Info and Tracks so that Track metadata
// and the timecode scale are available before the first block is read.
func New(src io.Reader) (*Demuxer, error) {
	d := &Demuxer{
		scaleNS: 1_000_000,
		tracks:  make(map[uint32]*Track),
		seq:     make(map[uint32]uint64),
	}
	cr := &countingReader{r: src, onRead: func(n int) { d.bytes += int64(n) }}
	d.r = ebml.NewReader(cr)
	if err := d.readPreamble(); err != nil {
		return nil, err
	}
	d.summary.TimecodeScaleNS = d.scaleNS
	for _, n := range d.order {
		tr := d.tracks[n]
		d.summary.Tracks = append(d.summary.Tracks, TrackInfo{
			Number: tr.Number, Type: tr.Type, CodecID: tr.CodecID,
		})
	}
	return d, nil
}

// readPreamble walks EBML header -> Segment -> Info/Tracks, tolerating
// unknown-length Segment encoding.
func (d *Demuxer) readPreamble() error {
	el, err := d.r.Next()
	if err != nil {
		return err
	}
	if el.ID != ebml.IDEBMLExtra {
		return fmt.Errorf("demux: expected EBML header, got element 0x%X at %d", el.ID, el.Offset)
	}
	if err := d.r.Enter(el); err != nil {
		return err
	}
	if err := d.skipContainer(); err != nil {
		return err
	}
	seg, err := d.r.Next()
	if err != nil {
		return err
	}
	if seg.ID != ebml.IDSegment {
		return fmt.Errorf("demux: expected Segment, got 0x%X", seg.ID)
	}
	if err := d.r.Enter(seg); err != nil {
		return err
	}
	for {
		el, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("demux: Segment ended before Tracks header")
		}
		if err != nil {
			return err
		}
		switch el.ID {
		case ebml.IDInfo:
			if err := d.readInfo(el); err != nil {
				return err
			}
		case ebml.IDTracks:
			if err := d.readTracks(el); err != nil {
				return err
			}
			if len(d.order) == 0 {
				return fmt.Errorf("demux: Tracks element declares no track entries")
			}
			return nil
		default:
			if el.Container {
				if err := d.r.Enter(el); err != nil {
					return err
				}
				if err := d.skipContainer(); err != nil {
					return err
				}
			} else if err := d.r.Skip(el); err != nil {
				return err
			}
		}
	}
}

func (d *Demuxer) skipContainer() error {
	for {
		el, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return d.r.Exit()
		}
		if err != nil {
			return err
		}
		if el.Container {
			if err := d.r.Enter(el); err != nil {
				return err
			}
			if err := d.skipContainer(); err != nil {
				return err
			}
		} else if err := d.r.Skip(el); err != nil {
			return err
		}
	}
}

func (d *Demuxer) readInfo(el *ebml.Element) error {
	if err := d.r.Enter(el); err != nil {
		return err
	}
	for {
		child, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return d.r.Exit()
		}
		if err != nil {
			return err
		}
		if child.ID == ebml.IDTimecodeScale {
			raw, err := d.r.ReadData(child)
			if err != nil {
				return err
			}
			v, err := ebml.Uint(raw)
			if err != nil {
				return err
			}
			d.scaleNS = int64(v)
		} else if err := d.r.Skip(child); err != nil {
			return err
		}
	}
}

func (d *Demuxer) readTracks(el *ebml.Element) error {
	if err := d.r.Enter(el); err != nil {
		return err
	}
	for {
		entry, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return d.r.Exit()
		}
		if err != nil {
			return err
		}
		if entry.ID != ebml.IDTrackEntry {
			if err := d.r.Skip(entry); err != nil {
				return err
			}
			continue
		}
		tr, err := d.readTrackEntry(entry)
		if err != nil {
			return err
		}
		if _, dup := d.tracks[tr.Number]; dup {
			return fmt.Errorf("demux: duplicate TrackNumber %d", tr.Number)
		}
		tr.TimecodeScaleNS = d.scaleNS
		d.tracks[tr.Number] = tr
		d.order = append(d.order, tr.Number)
	}
}

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

// Tracks returns parsed track metadata in declaration order.
func (d *Demuxer) Tracks() []*Track {
	out := make([]*Track, 0, len(d.order))
	for _, n := range d.order {
		out = append(out, d.tracks[n])
	}
	return out
}

// ScaleNS returns the container TimecodeScale in nanoseconds.
func (d *Demuxer) ScaleNS() int64 { return d.scaleNS }
