// Package mp4scan walks MP4/MOV containers, validates box structure, and
// builds per-track sample tables (decode time, file offset, size) from the
// stts/stsc/stco/stsz boxes. Structural corruption is reported as
// ERR_CORRUPT_BOX with the byte offset of the offending box.
package mp4scan

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediakernel/internal/fault"
)

// Sample is one media sample located inside the container.
type Sample struct {
	Index  uint32 // sample number within the track, 0-based
	DTS    int64  // decode timestamp in track ticks
	Offset int64  // absolute file offset of the sample payload
	Size   uint32 // sample payload size in bytes
	Key    bool   // sync (keyframe) sample
}

// Track is the scanned sample table of one track.
type Track struct {
	ID        uint32
	Timescale uint32
	Samples   []Sample
}

// Container is the scan result: every track with its sample table.
type Container struct {
	Tracks []Track
}

// rawTrack accumulates the boxes of one trak while walking.
type rawTrack struct {
	id        uint32
	timescale uint32
	stts      *mp4.Stts
	stsc      *mp4.Stsc
	stsz      *mp4.Stsz
	stco      []uint64
	stss      map[uint32]bool
}

// Scan parses the container in r and builds sample tables for all tracks.
func Scan(r io.ReadSeeker) (*Container, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	c := &Container{}
	var cur *rawTrack
	err = walk(r, 0, size, func(bi *mp4.BoxInfo, depth int) (bool, error) {
		switch bi.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeDinf():
			return true, nil
		case mp4.BoxTypeTrak():
			cur = &rawTrack{stss: map[uint32]bool{}}
			return true, nil
		}
		if cur == nil {
			return false, nil
		}
		switch bi.Type {
		case mp4.BoxTypeTkhd():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.id = p.(*mp4.Tkhd).TrackID
		case mp4.BoxTypeMdhd():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.timescale = p.(*mp4.Mdhd).Timescale
		case mp4.BoxTypeStts():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.stts = p.(*mp4.Stts)
		case mp4.BoxTypeStsc():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.stsc = p.(*mp4.Stsc)
		case mp4.BoxTypeStsz():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.stsz = p.(*mp4.Stsz)
		case mp4.BoxTypeStco():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			for _, o := range p.(*mp4.Stco).ChunkOffset {
				cur.stco = append(cur.stco, uint64(o))
			}
		case mp4.BoxTypeCo64():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			cur.stco = append(cur.stco, p.(*mp4.Co64).ChunkOffset...)
		case mp4.BoxTypeStss():
			p, err := readPayload(r, bi)
			if err != nil {
				return false, err
			}
			for _, n := range p.(*mp4.Stss).SampleNumber {
				cur.stss[n] = true
			}
		}
		return false, nil
	}, func(bi *mp4.BoxInfo) error {
		if bi.Type == mp4.BoxTypeTrak() && cur != nil {
			t, err := buildTrack(cur)
			if err != nil {
				return err
			}
			c.Tracks = append(c.Tracks, *t)
			cur = nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// walk traverses boxes in [start, end), invoking enter for each box (which
// returns whether to recurse) and leave after a container's children.
func walk(r io.ReadSeeker, start, end int64, enter func(*mp4.BoxInfo, int) (bool, error), leave func(*mp4.BoxInfo) error) error {
	pos := start
	for pos < end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return fault.New(fault.ErrCorruptBox, pos, "box header unreadable: %v", err)
		}
		boxOff := int64(bi.Offset)
		if bi.Size > uint64(1<<62) || bi.Size < bi.HeaderSize {
			return fault.New(fault.ErrCorruptBox, boxOff,
				"box %s implausible size %d", bi.Type.String(), bi.Size)
		}
		boxEnd := int64(bi.Size) + boxOff
		if bi.ExtendToEOF {
			boxEnd = end
		} else if boxEnd > end {
			return fault.New(fault.ErrCorruptBox, boxOff,
				"box %s size %d exceeds parent bounds [%d,%d)", bi.Type.String(), bi.Size, start, end)
		}
		recurse, err := enter(bi, 0)
		if err != nil {
			return err
		}
		if recurse {
			if err := walk(r, boxOff+int64(bi.HeaderSize), boxEnd, enter, leave); err != nil {
				return err
			}
			if err := leave(bi); err != nil {
				return err
			}
		}
		pos = boxEnd
	}
	return nil
}

func readPayload(r io.ReadSeeker, bi *mp4.BoxInfo) (mp4.IBox, error) {
	if _, err := r.Seek(int64(bi.Offset)+int64(bi.HeaderSize), io.SeekStart); err != nil {
		return nil, err
	}
	p, _, err := mp4.UnmarshalAny(r, bi.Type, bi.Size-uint64(bi.HeaderSize), mp4.Context{})
	if err != nil {
		return nil, fault.New(fault.ErrCorruptBox, int64(bi.Offset),
			"box %s payload unreadable: %v", bi.Type.String(), err)
	}
	return p, nil
}
