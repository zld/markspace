package fixture

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

type seekBuffer struct {
	b   []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.b)) {
		grown := make([]byte, end)
		copy(grown, s.b)
		s.b = grown
	}
	copy(s.b[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = s.pos + offset
	case io.SeekEnd:
		next = int64(len(s.b)) + offset
	default:
		return 0, fmt.Errorf("unsupported whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("negative seek position")
	}
	s.pos = next
	return next, nil
}

func (s *seekBuffer) Bytes() []byte { return append([]byte(nil), s.b...) }

type boxWriter struct {
	w *mp4.Writer
}

func (b *boxWriter) start(typ mp4.BoxType) (*mp4.BoxInfo, error) {
	return b.w.StartBox(&mp4.BoxInfo{Type: typ})
}

func (b *boxWriter) marshal(box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(b.w, box, mp4.Context{})
	return err
}

func (b *boxWriter) end() error {
	_, err := b.w.EndBox()
	return err
}

func buildMP4(reps []*RepSpec) ([]byte, map[uint32]int64, error) {
	var sb seekBuffer
	bw := &boxWriter{w: mp4.NewWriter(&sb)}

	if _, err := bw.start(mp4.BoxTypeFtyp()); err != nil {
		return nil, nil, err
	}
	if err := bw.marshal(&mp4.Ftyp{
		MajorBrand:   mp4.BrandISOM(),
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISOM()},
			{CompatibleBrand: mp4.BrandMP41()},
		},
	}); err != nil {
		return nil, nil, err
	}
	if err := bw.end(); err != nil {
		return nil, nil, err
	}

	if _, err := bw.start(mp4.BoxTypeMoov()); err != nil {
		return nil, nil, err
	}
	sttsOffsets := map[uint32]int64{}
	for _, rep := range reps {
		if err := writeTrak(bw, rep, sttsOffsets); err != nil {
			return nil, nil, err
		}
	}
	if err := bw.end(); err != nil {
		return nil, nil, err
	}
	return sb.Bytes(), sttsOffsets, nil
}

func writeTrak(bw *boxWriter, rep *RepSpec, sttsOffsets map[uint32]int64) error {
	if _, err := bw.start(mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if _, err := bw.start(mp4.BoxTypeTkhd()); err != nil {
		return err
	}
	if err := bw.marshal(&mp4.Tkhd{
		FullBox:        mp4.FullBox{Version: 0},
		TrackID:        rep.TrackID,
		DurationV0:     0,
		AlternateGroup: 0,
		Volume:         0,
		Width:          1920 << 16,
		Height:         1080 << 16,
	}); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}

	if _, err := bw.start(mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if _, err := bw.start(mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	var duration uint64
	if n := len(rep.Samples); n > 0 {
		duration = rep.Samples[n-1].Ticks + uint64(rep.Samples[n-1].Delta)
	}
	if err := bw.marshal(&mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 1},
		Timescale:  rep.Timescale,
		DurationV1: duration,
		Language:   [3]byte{'u' - 0x60, 'n' - 0x60, 'd' - 0x60},
	}); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}
	if _, err := bw.start(mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	if err := bw.marshal(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: rep.Name}); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return err
	}

	if _, err := bw.start(mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if _, err := bw.start(mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := writeStts(bw, rep, sttsOffsets); err != nil {
		return err
	}
	if err := writeStss(bw, rep); err != nil {
		return err
	}
	if err := bw.end(); err != nil { // stbl
		return err
	}
	if err := bw.end(); err != nil { // minf
		return err
	}
	if err := bw.end(); err != nil { // mdia
		return err
	}
	return bw.end() // trak
}

func writeStts(bw *boxWriter, rep *RepSpec, offsets map[uint32]int64) error {
	bi, err := bw.start(mp4.BoxTypeStts())
	if err != nil {
		return err
	}
	offsets[rep.TrackID] = int64(bi.Offset)
	entries := make([]mp4.SttsEntry, 0, len(rep.Samples))
	for _, s := range rep.Samples {
		entries = append(entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: s.Delta})
	}
	if err := bw.marshal(&mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}); err != nil {
		return err
	}
	return bw.end()
}

func writeStss(bw *boxWriter, rep *RepSpec) error {
	if _, err := bw.start(mp4.BoxTypeStss()); err != nil {
		return err
	}
	var nums []uint32
	for _, s := range rep.Samples {
		if s.Keyframe {
			nums = append(nums, s.SampleNumber)
		}
	}
	if err := bw.marshal(&mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}); err != nil {
		return err
	}
	return bw.end()
}
