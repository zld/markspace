package fixture

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"

	"archivecheck/internal/media"
)

// writeSeeker is a growable in-memory io.WriteSeeker. go-mp4's
// EndBox seeks backwards to patch each box's size field, so arbitrary
// seeks must be supported, not append-only writes.
type writeSeeker struct {
	buf []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	if end := int(w.pos) + len(p); end > len(w.buf) {
		w.buf = append(w.buf, make([]byte, end-len(w.buf))...)
	}
	copy(w.buf[int(w.pos):], p)
	w.pos += int64(len(p))
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = w.pos + offset
	case io.SeekEnd:
		pos = int64(len(w.buf)) + offset
	}
	if pos < 0 {
		return 0, io.EOF
	}
	w.pos = pos
	return pos, nil
}

func (w *writeSeeker) Bytes() []byte { return w.buf }

// segmentMeta is the deterministic sidecar accompanying each segment,
// carrying the facts the transport layer needs (loss, truncation and
// the authoritative frame count) without any remote lookup.
type segmentMeta struct {
	TrackID     uint32 `json:"track_id"`
	Seq         uint32 `json:"seq"`
	FrameCount  int    `json:"frame_count"`
	MediaBytes  int    `json:"media_bytes"`
	LostPackets int    `json:"lost_packets"`
	Truncated   bool   `json:"truncated"`
	CorruptFrom int    `json:"corrupt_from_index"`
}

// buildSegmentBytes serializes an fMP4 subset: styp, moof(traf) and mdat.
// The trun DataOffset is computed from the fixed box layout so every
// generated segment is byte-identical for identical inputs.
func buildSegmentBytes(spec media.TrackSpec, seq uint32, frames [][]byte,
	corruptFrom int, truncated bool) ([]byte, error) {

	ws := &writeSeeker{}
	w := mp4.NewWriter(ws)

	styp := &mp4.Styp{
		MajorBrand:   [4]byte{'m', 's', 'd', 'h'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}},
			{CompatibleBrand: [4]byte{'m', 's', 'i', 'x'}},
		},
	}
	if _, err := startBox(w, mp4.BoxTypeStyp()); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, styp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return nil, err
	}
	mfhd := &mp4.Mfhd{SequenceNumber: seq}
	if err := marshalChild(w, mp4.BoxTypeMfhd(), mfhd); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return nil, err
	}
	tfhd := &mp4.Tfhd{}
	tfhd.SetFlags(mp4.TfhdDefaultSampleDurationPresent |
		mp4.TfhdDefaultSampleSizePresent | mp4.TfhdDefaultBaseIsMoof)
	tfhd.TrackID = spec.ID
	tfhd.DefaultSampleDuration = uint32(media.ClockRate / FramesPerSegment)
	if err := marshalChild(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
		return nil, err
	}
	tfdt := &mp4.Tfdt{}
	tfdt.SetVersion(1)
	tfdt.BaseMediaDecodeTimeV1 = uint64(seq-1) * uint64(media.ClockRate) * SegmentSeconds
	if err := marshalChild(w, mp4.BoxTypeTfdt(), tfdt); err != nil {
		return nil, err
	}
	trun := &mp4.Trun{}
	trun.SetFlags(0x000001 | 0x000200) // data-offset + sample-size present
	trun.SampleCount = uint32(len(frames))
	trun.DataOffset = int32(moofHeaderSize(len(frames)))
	for _, f := range frames {
		trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleSize: uint32(len(f))})
	}
	if err := marshalChild(w, mp4.BoxTypeTrun(), trun); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return nil, err
	}

	// mdat carries the concatenated frame payloads. A truncated segment
	// physically ends inside the last decodable frame's bytes.
	payload := new(bytes.Buffer)
	usable := len(frames)
	if truncated {
		usable = corruptFrom
	}
	for i := 0; i < usable; i++ {
		payload.Write(frames[i])
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	mdat := &mp4.Mdat{Data: payload.Bytes()}
	if _, err := mp4.Marshal(w, mdat, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return ws.Bytes(), nil
}

func startBox(w *mp4.Writer, t mp4.BoxType) (*mp4.BoxInfo, error) {
	return w.StartBox(&mp4.BoxInfo{Type: t})
}

func marshalChild(w *mp4.Writer, t mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: t}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}

// moofHeaderSize returns the exact byte length up to and including the
// moof/mdat headers, giving the trun data-offset (first sample start).
func moofHeaderSize(sampleCount int) int {
	mfhdBox := 8 + 4
	tfhdBox := 8 + 4 + 4 + 4 + 4
	tfdtBox := 8 + 4 + 8
	trunBox := 8 + 4 + 4 + 4 + sampleCount*4
	trafBox := 8 + tfhdBox + tfdtBox + trunBox
	moofBox := 8 + mfhdBox + trafBox
	return moofBox + 8 // plus the mdat header
}
