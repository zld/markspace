package pipeline

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"regress-kernel/internal/media"
)

// FramesPerSegment fixes the fMP4 fragment boundary; deterministic by design.
const FramesPerSegment = 10

// memWriteSeeker is a full in-memory io.WriteSeeker: go-mp4's EndBox seeks
// backwards to patch box headers, so appending-only buffers are insufficient.
type memWriteSeeker struct {
	data []byte
	pos  int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	if end := m.pos + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	copy(m.data[m.pos:], p)
	m.pos += int64(len(p))
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = m.pos + offset
	case io.SeekEnd:
		next = int64(len(m.data)) + offset
	}
	if next < 0 {
		return 0, fmt.Errorf("negative seek position %d", next)
	}
	m.pos = next
	return next, nil
}

// Bytes returns the accumulated box bytes.
func (m *memWriteSeeker) Bytes() []byte { return m.data }

// SegmentInfo is what the parser proves about a rendered segment file.
type SegmentInfo struct {
	SequenceNumber uint32
	BaseMediaTime  int64
	SampleCount    int
	SampleSizes    []uint32
	Size           int64
}

func brand(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}

// marshalBox wraps one leaf box: StartBox emits the size/type header, Marshal
// writes only the payload and EndBox patches the final size back in.
func marshalBox(w *mp4.Writer, boxType mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// marshalMoof writes one moof/traf hierarchy onto an existing writer.
// dataOffset is measured from the first byte of the moof box, as ISO BMFF
// requires, and points at the first mdat sample payload.
func marshalMoof(w *mp4.Writer, seq uint32, baseMediaTime int64,
	frames []media.Frame, dataOffset uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeMfhd(), &mp4.Mfhd{SequenceNumber: seq}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	tfhd := &mp4.Tfhd{TrackID: 1}
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof) // 0x020000: data base is the moof box
	if err := marshalBox(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
		return err
	}
	tfdtBox := &mp4.Tfdt{}
	tfdtBox.SetVersion(1) // 64-bit base media decode time
	tfdtBox.BaseMediaDecodeTimeV1 = uint64(baseMediaTime)
	if err := marshalBox(w, mp4.BoxTypeTfdt(), tfdtBox); err != nil {
		return err
	}
	trun := &mp4.Trun{SampleCount: uint32(len(frames))}
	// 0x000001: data-offset present; 0x000200: per-sample size present.
	trun.SetFlags(0x000001 | 0x000200)
	trun.DataOffset = int32(dataOffset)
	for _, f := range frames {
		trun.Entries = append(trun.Entries,
			mp4.TrunEntry{SampleSize: uint32(len(f.Payload))})
	}
	if err := marshalBox(w, mp4.BoxTypeTrun(), trun); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return err
	}
	return nil
}

// moofSize renders the fragment boxes alone to measure their serialized size.
func moofSize(seq uint32, baseMediaTime int64, frames []media.Frame) (int, error) {
	probe := &memWriteSeeker{}
	if err := marshalMoof(mp4.NewWriter(probe), seq, baseMediaTime, frames, 0); err != nil {
		return 0, err
	}
	return len(probe.Bytes()), nil
}

// RenderSegment builds a deterministic fragmented MP4 segment from frames.
// Two passes are required because trun.dataOffset depends on the final moof
// size; the same frames therefore always produce the identical byte stream.
func RenderSegment(seq uint32, baseMediaTime int64,
	frames []media.Frame) ([]byte, error) {
	moofLen, err := moofSize(seq, baseMediaTime, frames)
	if err != nil {
		return nil, err
	}
	// dataOffset spans the whole moof and the 8-byte mdat header.
	dataOffset := uint32(moofLen) + 8

	out := &memWriteSeeker{}
	w := mp4.NewWriter(out)
	styp := &mp4.Styp{
		MajorBrand:   brand("msdh"),
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: brand("msdh")},
			{CompatibleBrand: brand("msix")},
		},
	}
	if err := marshalBox(w, mp4.BoxTypeStyp(), styp); err != nil {
		return nil, err
	}
	if err := marshalMoof(w, seq, baseMediaTime, frames, dataOffset); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	for _, f := range frames {
		if _, err := w.Write(f.Payload); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ParseSegment walks the fMP4 boxes and returns the fragment metadata needed
// to verify continuity between adjacent segments.
func ParseSegment(r io.ReadSeeker) (*SegmentInfo, error) {
	info := &SegmentInfo{}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof(), mp4.BoxTypeTraf():
			return h.Expand() // recurse into fragment containers
		case mp4.BoxTypeMfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.SequenceNumber = box.(*mp4.Mfhd).SequenceNumber
		case mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.BaseMediaTime = int64(box.(*mp4.Tfdt).GetBaseMediaDecodeTime())
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tr := box.(*mp4.Trun)
			info.SampleCount = int(tr.SampleCount)
			for _, e := range tr.Entries {
				info.SampleSizes = append(info.SampleSizes, e.SampleSize)
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if info.SampleCount == 0 {
		return nil, fmt.Errorf("%w: segment without samples", media.ErrCheckpoint)
	}
	return info, nil
}

// SegmentFrames groups a frame sequence into fixed-size fragments.
func SegmentFrames(frames []media.Frame) [][]media.Frame {
	var out [][]media.Frame
	for start := 0; start < len(frames); start += FramesPerSegment {
		end := start + FramesPerSegment
		if end > len(frames) {
			end = len(frames)
		}
		out = append(out, frames[start:end])
	}
	return out
}
