package dash

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

// boxScan collects the boxes needed from one fMP4 unit in one bounded pass.
type boxScan struct {
	boxCount  int
	meta      *mcidView
	sidxCount int
	mdatSize  int
	moofSeen  bool
	tfdtBase  uint64
	mfhdSeq   uint32
	truncated bool
	parseErr  error
}

type mcidView struct {
	timescale      uint32
	channels       uint16
	sampleRate     uint32
	colorPrimaries uint16
	transferChar   uint16
	matrixCoeffs   uint16
	segmentSeq     uint32
	baseDecode     uint64
	indexPresent   uint8
}

// boundedReadSeeker caps how many bytes a single parse may consume. go-mp4
// seeks backwards to patch/read box headers, so the budget is recomputed from
// the absolute position on every seek rather than consumed monotonically.
type boundedReadSeeker struct {
	*bytes.Reader
	capBytes int64
}

func (b *boundedReadSeeker) Read(p []byte) (int, error) {
	pos, _ := b.Reader.Seek(0, io.SeekCurrent)
	// One byte of headroom: a box payload ending exactly at the limit is a
	// complete read, while a declared size beyond the data is caught
	// separately via the box-offset truncation check.
	avail := b.capBytes + 1 - pos
	if avail <= 0 {
		return 0, model.ErrMemoryLimit
	}
	if int64(len(p)) > avail {
		p = p[:avail]
	}
	return b.Reader.Read(p)
}

func newBounded(data []byte, lim safeio.Limits) *boundedReadSeeker {
	capBytes := lim.MaxParseBytes
	if int64(len(data)) < capBytes {
		capBytes = int64(len(data))
	}
	return &boundedReadSeeker{Reader: bytes.NewReader(data), capBytes: capBytes}
}

// ParseInit extracts metadata from an initialization segment.
func ParseInit(data []byte) (InitMeta, error) {
	scan, err := scanBoxes(data, safeio.DefaultLimits())
	if err != nil {
		return InitMeta{}, err
	}
	if scan.meta == nil {
		return InitMeta{}, fmt.Errorf("init segment missing metadata box")
	}
	mv := scan.meta
	return InitMeta{
		Timescale:      int64(mv.timescale),
		Channels:       int(mv.channels),
		SampleRate:     int(mv.sampleRate),
		ColorPrimaries: mv.colorPrimaries,
		TransferChar:   mv.transferChar,
		MatrixCoeffs:   mv.matrixCoeffs,
	}, nil
}

// ParseSegment parses one media segment and detects structural truncation.
func ParseSegment(fr *fixture.Representation, seg *fixture.Segment, lim safeio.Limits) (*SegmentRecord, error) {
	if int64(len(seg.Data)) > lim.MaxParseBytes {
		return nil, model.ErrMemoryLimit
	}
	sr := &SegmentRecord{
		Representation: fr.ID, Sequence: seg.Sequence,
		Bytes: len(seg.Data), IndexPresent: seg.IndexPresent,
		SampleCount: len(seg.Samples),
	}
	for _, s := range seg.Samples {
		if s.IsKeyframe {
			sr.KeyframeCount++
		}
	}
	scan, err := scanBoxes(seg.Data, lim)
	if err != nil {
		// A short read or budget stop inside the box tree is the on-disk
		// signature of a truncated segment: classify instead of aborting.
		if isTruncation(err) || errors.Is(err, model.ErrMemoryLimit) {
			sr.Truncated = true
			return sr, nil
		}
		return nil, err
	}
	sr.Truncated = scan.truncated || seg.Truncated
	if !scan.moofSeen {
		sr.Truncated = true
	}
	// No sidx: consumers can only locate the whole segment as one interval.
	if scan.sidxCount == 0 {
		sr.LocatableOnly = true
	}
	return sr, nil
}

func scanBoxes(data []byte, lim safeio.Limits) (*boxScan, error) {
	scan := &boxScan{}
	// A physically cut segment cannot satisfy its declared box sizes; treat
	// any declared overrun up front as truncation rather than a hard limit.
	r := newBounded(data, lim)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		scan.boxCount++
		if scan.boxCount > lim.MaxBoxes {
			return nil, model.ErrMemoryLimit
		}
		switch h.BoxInfo.Type {
		case mp4.BoxTypeSidx():
			scan.sidxCount++
		case mp4.BoxTypeMoof():
			scan.moofSeen = true
		case mp4.BoxTypeMdat():
			scan.mdatSize = int(h.BoxInfo.Size)
		case mp4.BoxTypeMfhd():
			if box, _, err := h.ReadPayload(); err == nil {
				if mfhd, ok := box.(*mp4.Mfhd); ok {
					scan.mfhdSeq = mfhd.SequenceNumber
				}
			}
		case mp4.BoxTypeTfdt():
			if box, _, err := h.ReadPayload(); err == nil {
				if tfdt, ok := box.(*mp4.Tfdt); ok {
					scan.tfdtBase = tfdt.GetBaseMediaDecodeTime()
				}
			}
		case mp4.StrToBoxType("mcid"):
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mv := readMCID(box); mv != nil {
				scan.meta = mv
			}
		}
		// Detect a declared box size extending past the available bytes.
		end := int64(h.BoxInfo.Offset) + int64(h.BoxInfo.Size)
		if end > int64(len(data)) {
			scan.truncated = true
		}
		// Only container boxes have children; expanding a leaf makes
		// go-mp4 seek past the payload and trip the bounded reader.
		if isContainer(h.BoxInfo.Type) {
			return h.Expand()
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		if isTruncation(err) {
			scan.truncated = true
			return scan, nil
		}
		return scan, err
	}
	return scan, nil
}

// readMCID reflects over the fixture's registered custom box without an
// import cycle (fixture already imports dash-free packages; dash may import
// fixture, so a direct type assertion is used here).
func readMCID(box mp4.IBox) *mcidView {
	mc, ok := box.(*fixture.MCID)
	if !ok {
		return nil
	}
	return &mcidView{
		timescale:      mc.Timescale,
		channels:       mc.TrackChannels,
		sampleRate:     mc.SampleRate,
		colorPrimaries: mc.ColorPrimaries,
		transferChar:   mc.TransferChar,
		matrixCoeffs:   mc.MatrixCoeffs,
		segmentSeq:     mc.SegmentSeq,
		baseDecode:     mc.BaseDecodeTime,
		indexPresent:   mc.IndexPresent,
	}
}

func isContainer(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeMoof(),
		mp4.BoxTypeTraf(), mp4.BoxTypeEdts(), mp4.BoxTypeDinf():
		return true
	default:
		return false
	}
}

func isTruncation(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return true
	}
	return err == model.ErrTruncated
}
