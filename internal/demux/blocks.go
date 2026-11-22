package demux

import (
	"errors"
	"fmt"
	"io"

	"mediasync/internal/ebml"
)

// countingReader observes read sizes so the input summary can state how many
// bytes were actually consumed, including on truncated input.
type countingReader struct {
	r      io.Reader
	onRead func(int)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.onRead != nil {
		c.onRead(n)
	}
	return n, err
}

// Next returns the next block sample in file order, descending into clusters
// (known or unknown length) and transparently interleaving tracks. io.EOF
// marks a clean end; a truncated stream yields the *ebml.Error plus whatever
// samples preceded it through Summary.
func (d *Demuxer) Next() (*Sample, error) {
	for {
		el, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			// A cluster reached its boundary (known length or parent EOF):
			// pop both the demuxer frame and the EBML reader frame, then
			// continue scanning siblings inside the Segment.
			if len(d.clusters) > 0 {
				if err := d.r.Exit(); err != nil {
					return nil, err
				}
				d.popCluster()
				continue
			}
			d.ended = true
			d.summary.Bytes = d.bytes
			return nil, io.EOF
		}
		if err != nil {
			var ee *ebml.Error
			if errors.As(err, &ee) {
				d.summary.Truncated = ee
				d.summary.Bytes = d.bytes
			}
			return nil, err
		}
		switch el.ID {
		case ebml.IDCluster:
			if err := d.r.Enter(el); err != nil {
				return nil, err
			}
			d.clusters = append(d.clusters, clusterFrame{end: el.DataOff + el.Size})
			d.clusterTC = 0
		case ebml.IDTimecode:
			raw, err := d.r.ReadData(el)
			if err != nil {
				return nil, d.captureTruncation(err)
			}
			v, err := ebml.Uint(raw)
			if err != nil {
				return nil, err
			}
			d.clusterTC = int64(v)
		case ebml.IDSimpleBlock:
			return d.readBlock(el, true)
		case ebml.IDBlockGroup:
			s, err := d.readBlockGroup(el)
			if err != nil {
				return nil, err
			}
			return s, nil
		default:
			if el.Container {
				if err := d.r.Enter(el); err != nil {
					return nil, err
				}
				if err := d.skipContainer(); err != nil {
					return nil, err
				}
			} else if err := d.r.Skip(el); err != nil {
				return nil, d.captureTruncation(err)
			}
		}
	}
}

// popCluster closes the innermost cluster frame. Returns false when no
// cluster is open (the Segment itself ended).
func (d *Demuxer) popCluster() bool {
	if len(d.clusters) == 0 {
		return false
	}
	d.clusters = d.clusters[:len(d.clusters)-1]
	return true
}

// readBlockGroup contains one Block plus reference/extension children that
// the lossless path does not interpret; the Block is extracted and the rest
// skipped.
func (d *Demuxer) readBlockGroup(el *ebml.Element) (*Sample, error) {
	if err := d.r.Enter(el); err != nil {
		return nil, err
	}
	for {
		child, err := d.r.Next()
		if errors.Is(err, io.EOF) {
			return nil, d.r.Exit()
		}
		if err != nil {
			return nil, d.captureTruncation(err)
		}
		if child.ID == ebml.IDBlock {
			s, err := d.readBlock(child, false)
			if err != nil {
				return nil, err
			}
			// Drain remaining siblings before returning to the outer loop.
			if err := d.skipContainer(); err != nil {
				return nil, err
			}
			return s, nil
		}
		if err := d.r.Skip(child); err != nil {
			return nil, d.captureTruncation(err)
		}
	}
}

// readBlock parses the Matroska Block header (track VINT, signed 16-bit
// relative timecode, flags) and returns the untouched codec payload. Lacing
// is rejected: spliced delivery keeps one sample per block.
func (d *Demuxer) readBlock(el *ebml.Element, simple bool) (*Sample, error) {
	data, err := d.r.ReadData(el)
	if err != nil {
		return nil, d.captureTruncation(err)
	}
	track, n, err := readTrackVINT(data)
	if err != nil {
		return nil, fmt.Errorf("demux: block at offset %d: %w", el.Offset, err)
	}
	if len(data) < n+3 {
		return nil, d.captureTruncation(fmt.Errorf("demux: block header truncated at offset %d", el.Offset))
	}
	rel := int16(uint16(data[n])<<8 | uint16(data[n+1]))
	flags := data[n+2]
	lacing := (flags >> 1) & 0x03
	if lacing != 0 {
		return nil, fmt.Errorf("demux: laced blocks are not supported (track=%d offset=%d)", track, el.Offset)
	}
	if _, known := d.tracks[track]; !known {
		return nil, fmt.Errorf("demux: block references undeclared track %d at offset %d", track, el.Offset)
	}
	d.seq[track]++
	seq := d.seq[track]
	d.summary.TotalBlocks++
	if ti := d.trackInfo(track); ti != nil {
		ti.Blocks++
	}
	return &Sample{
		Track:     track,
		Seq:       seq,
		TimeTicks: d.clusterTC + int64(rel),
		Keyframe:  simple && flags&0x80 != 0,
		Payload:   append([]byte(nil), data[n+3:]...),
	}, nil
}

func (d *Demuxer) trackInfo(track uint32) *TrackInfo {
	for i := range d.summary.Tracks {
		if d.summary.Tracks[i].Number == track {
			return &d.summary.Tracks[i]
		}
	}
	return nil
}

// readTrackVINT decodes the leading block track-number VINT.
func readTrackVINT(data []byte) (uint32, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("empty block")
	}
	width := 1
	for mask := byte(0x80); data[0]&mask == 0; mask >>= 1 {
		width++
		if width > 4 || width > len(data) {
			return 0, 0, fmt.Errorf("bad track VINT width %d", width)
		}
	}
	var v uint32
	for i := 0; i < width; i++ {
		v = v<<8 | uint32(data[i])
	}
	// Strip the VINT length marker: clear the highest set bit of byte 0.
	v ^= uint32(1) << uint(8*width-1)
	return v, width, nil
}

func (d *Demuxer) captureTruncation(err error) error {
	var ee *ebml.Error
	if errors.As(err, &ee) {
		d.summary.Truncated = ee
		d.summary.Bytes = d.bytes
	}
	return err
}

// Summary returns the input summary, valid after Next returns io.EOF or an
// error so callers retain diagnostics for damaged fixtures.
func (d *Demuxer) Summary() Summary {
	s := d.summary
	s.Bytes = d.bytes
	return s
}
