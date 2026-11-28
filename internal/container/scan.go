// Package container parses self-generated MP4/MOV regression fixtures.
//
// It uses github.com/abema/go-mp4 for box headers and sample-table payload
// decoding while owning the recursive walk itself, so truncation is reported
// at the exact byte where the read fails. No damaged box or sample is ever
// skipped silently: the first out-of-bounds position becomes a fault.Error.
package container

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"rtptrace/internal/fault"
)

// Sample is one media sample with its file range and decode timestamp.
type Sample struct {
	Index  int    // 0-based sample index inside the track
	Offset uint64 // absolute byte offset of the sample data
	Size   uint64
	DTS    uint64 // decode time in track timescale units
	Sync   bool   // keyframe / sync sample
}

// Track is a parsed trak box.
type Track struct {
	Index      int // 0-based walk order
	TrackID    uint32
	Timescale  uint32
	Samples    []Sample
	SampleCount int
}

// MdatRange is the payload range of one mdat box.
type MdatRange struct {
	PayloadStart uint64
	PayloadEnd   uint64
}

// Container is the parsed result for one input file.
type Container struct {
	Path     string
	FileSize uint64
	SHA256   string
	Mdat     []MdatRange
	Tracks   []*Track
}

// tableRef remembers where a sample table lives so entry offsets are exact.
type tableRef struct {
	boxOffset  uint64
	headerSize uint64
}

type rawTrack struct {
	trackID   uint32
	timescale uint32
	stts      *mp4.Stts
	sttsRef   tableRef
	stsz      *mp4.Stsz
	stszRef   tableRef
	stsc      *mp4.Stsc
	stscRef   tableRef
	offsets   []uint64
	coRef     tableRef
	co64      bool
	stss      *mp4.Stss
	stssRef   tableRef
}

// ScanFile parses path and validates every sample range against the file.
func ScanFile(path string) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.KindInternal, "file", fault.NoOffset, "open %s: %v", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, fault.New(fault.KindInternal, "file", fault.NoOffset, "stat: %v", err)
	}
	c := &Container{Path: path, FileSize: uint64(st.Size())}

	digest, err := digestFile(f)
	if err != nil {
		return nil, err
	}
	c.SHA256 = digest

	// digestFile leaves the handle at EOF; the walk must start at zero.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fault.New(fault.KindInternal, "file", fault.NoOffset, "rewind: %v", err)
	}
	var raws []*rawTrack
	var stack []*rawTrack
	if err := walkChildren(f, int64(c.FileSize), true, &stack, &raws, c); err != nil {
		return nil, err
	}
	if len(raws) == 0 {
		return nil, fault.New(fault.KindCorrupt, "moov/trak", fault.NoOffset, "no tracks found")
	}
	for i, rt := range raws {
		tr := &Track{Index: i, TrackID: rt.trackID, Timescale: rt.timescale}
		if err := buildSamples(tr, rt, c); err != nil {
			return nil, err
		}
		c.Tracks = append(c.Tracks, tr)
	}
	return c, nil
}

func digestFile(r io.ReadSeeker) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", fault.New(fault.KindInternal, "file", fault.NoOffset, "seek: %v", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fault.New(fault.KindCorrupt, "file", fault.NoOffset, "read for digest: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// walkChildren walks boxes until parentEnd. Top level tolerates a clean EOF
// exactly at parentEnd; every other short read is reported as truncation.
func walkChildren(r io.ReadSeeker, parentEnd int64, top bool, stack *[]*rawTrack, raws *[]*rawTrack, c *Container) error {
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return fault.New(fault.KindInternal, "seek", fault.NoOffset, "%v", err)
		}
		if pos >= parentEnd {
			return nil
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			if top && err == io.EOF && pos == parentEnd {
				return nil
			}
			return fault.New(fault.KindCorrupt, "box.header", pos,
				"truncated box header at %d: %v", pos, err)
		}
		if bi.Size < bi.HeaderSize {
			return fault.New(fault.KindCorrupt, "box.size", int64(bi.Offset),
				"box %s size %d smaller than header %d", bi.Type.String(), bi.Size, bi.HeaderSize)
		}
		end := int64(bi.Offset + bi.Size)
		if !top && end > parentEnd {
			return fault.New(fault.KindCorrupt, "box.size", int64(bi.Offset),
				"box %s size %d exceeds parent end %d", bi.Type.String(), end, parentEnd)
		}
		if err := dispatch(r, bi, stack, raws, c); err != nil {
			return err
		}
		if _, err := r.Seek(end, io.SeekStart); err != nil {
			return fault.New(fault.KindCorrupt, "box", int64(bi.Offset),
				"cannot seek past box %s: %v", bi.Type.String(), err)
		}
	}
}

func dispatch(r io.ReadSeeker, bi *mp4.BoxInfo, stack *[]*rawTrack, raws *[]*rawTrack, c *Container) error {
	switch bi.Type {
	case mp4.BoxTypeMdat():
		c.Mdat = append(c.Mdat, MdatRange{
			PayloadStart: bi.Offset + bi.HeaderSize,
			PayloadEnd:   bi.Offset + bi.Size,
		})
		return nil
	case mp4.BoxTypeTrak():
		rt := &rawTrack{}
		*stack = append(*stack, rt)
		*raws = append(*raws, rt)
		err := walkChildren(r, int64(bi.Offset+bi.Size), false, stack, raws, c)
		*stack = (*stack)[:len(*stack)-1]
		return err
	case mp4.BoxTypeMdhd(), mp4.BoxTypeStts(), mp4.BoxTypeStsz(), mp4.BoxTypeStsc(),
		mp4.BoxTypeStco(), mp4.BoxTypeCo64(), mp4.BoxTypeStss():
		return parsePayload(r, bi, stack)
	default:
		// Container boxes we do not decode still need descending so nested
		// trak tables are reached; leaf boxes are simply skipped by the seek.
		if isContainer(bi.Type) {
			return walkChildren(r, int64(bi.Offset+bi.Size), false, stack, raws, c)
		}
		return nil
	}
}

func isContainer(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeDinf(), mp4.BoxTypeEdts(), mp4.BoxTypeUdta():
		return true
	}
	return false
}

func parsePayload(r io.ReadSeeker, bi *mp4.BoxInfo, stack *[]*rawTrack) error {
	payloadPos, err := bi.SeekToPayload(r)
	if err != nil {
		return fault.New(fault.KindCorrupt, "box."+bi.Type.String(), int64(bi.Offset), "%v", err)
	}
	size := bi.Size - bi.HeaderSize
	ref := tableRef{boxOffset: bi.Offset, headerSize: bi.HeaderSize}
	if len(*stack) == 0 {
		switch bi.Type {
		case mp4.BoxTypeMdhd():
			var v mp4.Mdhd
			if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
				return payloadFault(bi, payloadPos, err)
			}
		}
		return nil
	}
	rt := (*stack)[len(*stack)-1]
	switch bi.Type {
	case mp4.BoxTypeMdhd():
		var v mp4.Mdhd
		if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
			return payloadFault(bi, payloadPos, err)
		}
		if v.Timescale == 0 {
			return fault.New(fault.KindCorrupt, "mdhd.timescale", payloadPos, "zero timescale")
		}
		rt.timescale = v.Timescale
	case mp4.BoxTypeStts():
		var v mp4.Stts
		if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
			return payloadFault(bi, payloadPos, err)
		}
		rt.stts, rt.sttsRef = &v, ref
	case mp4.BoxTypeStsz():
		var v mp4.Stsz
		if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
			return payloadFault(bi, payloadPos, err)
		}
		rt.stsz, rt.stszRef = &v, ref
	case mp4.BoxTypeStsc():
		var v mp4.Stsc
		if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
			return payloadFault(bi, payloadPos, err)
		}
		rt.stsc, rt.stscRef = &v, ref
	case mp4.BoxTypeStco(), mp4.BoxTypeCo64():
		rt.co64 = bi.Type == mp4.BoxTypeCo64()
		if rt.co64 {
			var v mp4.Co64
			if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
				return payloadFault(bi, payloadPos, err)
			}
			rt.offsets, rt.coRef = v.ChunkOffset, ref
		} else {
			var v mp4.Stco
			if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
				return payloadFault(bi, payloadPos, err)
			}
			for _, o := range v.ChunkOffset {
				rt.offsets = append(rt.offsets, uint64(o))
			}
			rt.coRef = ref
		}
	case mp4.BoxTypeStss():
		var v mp4.Stss
		if _, err := mp4.Unmarshal(r, size, &v, bi.Context); err != nil {
			return payloadFault(bi, payloadPos, err)
		}
		rt.stss, rt.stssRef = &v, ref
	}
	return nil
}

func payloadFault(bi *mp4.BoxInfo, pos int64, err error) error {
	return fault.New(fault.KindCorrupt, "box."+bi.Type.String(), pos,
		"truncated payload: %v", err)
}

